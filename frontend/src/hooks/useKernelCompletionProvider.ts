import { useEffect, MutableRefObject } from 'react';
import { CompletionResult } from '@/hooks/useJupyterKernel';
import { NotebookCell } from '@/hooks/useNotebook';
import { cellModelMap } from '@/components/Notebooks/parts/CellEditor';

// Labels covered by the static providers (registered globally). We filter
// kernel results against these to avoid duplicate entries in the dropdown —
// the static versions have snippets + docstrings so they win.
const SPARK_SESSION_LABELS = [
    'read', 'readStream', 'sql', 'table', 'range', 'createDataFrame', 'createDataset',
    'emptyDataFrame', 'catalog', 'conf', 'sparkContext', 'streams', 'udf', 'version', 'stop',
];
const PYTHON_STATIC_LABELS = new Set<string>([
    'spark', 'pd', 'df', 'display', 'print',
    'select', 'filter', 'where', 'groupBy', 'join', 'withColumn', 'withColumnRenamed',
    'orderBy', 'show', 'count', 'head', 'describe', 'printSchema',
    'csv', 'parquet', 'json', 'table', 'load',
    'read_csv', 'DataFrame', 'to_datetime',
    ...SPARK_SESSION_LABELS,
]);
const SCALA_STATIC_LABELS = new Set<string>([
    'spark', 'sc', 'df', 'val', 'var', 'def', 'println',
    'select', 'filter', 'where', 'groupBy', 'join', 'withColumn', 'withColumnRenamed',
    'orderBy', 'show', 'count', 'head', 'describe', 'printSchema', 'cache', 'toDF',
    'csv', 'parquet', 'json', 'option', 'format', 'load',
    ...SPARK_SESSION_LABELS,
]);

type CachedCompletion = { result: CompletionResult; timestamp: number };
const CACHE_TTL = 30 * 1000;
const MAX_CACHE_SIZE = 100;

// Notebook shape the hook actually reads — just .cells. Kept loose to
// accept useNotebook's NotebookDetailDTO | null without tight coupling.
interface NotebookRefValue { cells?: NotebookCell[] | any[] }

type RequestCompletionFn = (code: string, cursorPos: number) => Promise<CompletionResult | null>;

export function useKernelCompletionProvider(
    monaco: any,
    requestCompletion: RequestCompletionFn | undefined,
    notebookRef: MutableRefObject<NotebookRefValue | null>,
) {
    useEffect(() => {
        if (!monaco || !requestCompletion) return;

        // Jupyter's metadata._jupyter_types_experimental[].type → Monaco icon.
        const mapKind = (type?: string): number => {
            const K = monaco.languages.CompletionItemKind;
            switch (type) {
                case 'function': return K.Function;
                case 'method':   return K.Method;
                case 'class':    return K.Class;
                case 'module':   return K.Module;
                case 'property': return K.Property;
                case 'param':    return K.Property;
                case 'keyword':  return K.Keyword;
                case 'statement': return K.Snippet;
                case 'magic':    return K.Text;
                case 'path':     return K.File;
                case 'instance': return K.Variable;
                default:         return K.Variable;
            }
        };

        // Completion cache keyed by the last ~50 chars before the cursor.
        // Short TTL so local variable additions surface quickly, but long
        // enough for typing-through-a-prefix (pd.r → pd.re → pd.rea) to hit.
        const cache = new Map<string, CachedCompletion>();

        // Build (fullCode, offset-in-fullCode) from the model by prepending all
        // preceding code cells' source. This lets the kernel's static analyzer
        // (Jedi for Python, Ammonite for Scala) see imports and top-level defs
        // from earlier cells without the user having to run them first.
        const buildFullCode = (model: any, offsetInCell: number) => {
            const cellId = cellModelMap.get(model);
            const cells = (notebookRef.current?.cells || []) as any[];
            const idx = cellId ? cells.findIndex((c: any) => c.id === cellId) : -1;
            let prefix = '';
            if (idx > 0) {
                prefix = cells
                    .slice(0, idx)
                    .filter((c: any) => (c.type || 'code').toLowerCase() === 'code')
                    .map((c: any) => c.source || '')
                    .join('\n\n');
                if (prefix) prefix += '\n\n';
            }
            return { fullCode: prefix + model.getValue(), fullOffset: prefix.length + offsetInCell, prefixLen: prefix.length };
        };

        const languages = ['python', 'scala'];
        const disposables: any[] = [];

        languages.forEach(lang => {
            const provider = monaco.languages.registerCompletionItemProvider(lang, {
                triggerCharacters: ['.', '(', '=', ' '],
                provideCompletionItems: async (model: any, position: any, _context: any, token: any) => {
                    const offsetInCell = model.getOffsetAt(position);
                    const { fullCode, fullOffset, prefixLen } = buildFullCode(model, offsetInCell);
                    const cacheKey = `${lang}|${prefixLen}|${fullCode.length}|${fullOffset}|${fullCode.slice(Math.max(0, fullOffset - 50), fullOffset)}`;

                    let result: CompletionResult | null;
                    const cached = cache.get(cacheKey);
                    if (cached && (Date.now() - cached.timestamp) < CACHE_TTL) {
                        result = cached.result;
                    } else {
                        try {
                            result = await requestCompletion(fullCode, fullOffset);
                        } catch {
                            return { suggestions: [] };
                        }
                        if (token.isCancellationRequested) return { suggestions: [] };
                        if (result) {
                            cache.set(cacheKey, { result, timestamp: Date.now() });
                            if (cache.size > MAX_CACHE_SIZE) {
                                const firstKey = cache.keys().next().value;
                                if (firstKey !== undefined) cache.delete(firstKey);
                            }
                        }
                    }

                    if (!result || result.status !== 'ok' || !result.matches.length) {
                        return { suggestions: [] };
                    }

                    const typedMeta = (result.metadata as any)?._jupyter_types_experimental as
                        | { text: string; type: string }[]
                        | undefined;
                    const kindByText = new Map<string, string>();
                    typedMeta?.forEach(m => { if (m?.text) kindByText.set(m.text, m.type); });

                    // Translate cursor_start/cursor_end back to the current cell's coordinate space.
                    const startInCell = Math.max(0, result.cursor_start - prefixLen);
                    const endInCell = Math.max(startInCell, result.cursor_end - prefixLen);

                    const skipLabels =
                        lang === 'python' ? PYTHON_STATIC_LABELS :
                        lang === 'scala' ? SCALA_STATIC_LABELS :
                        new Set<string>();
                    const suggestions = result.matches
                        .filter((match: string) => !skipLabels.has(match))
                        .map((match: string, index: number) => ({
                            label: match,
                            kind: mapKind(kindByText.get(match)),
                            insertText: match,
                            sortText: String(index).padStart(4, '0'),
                            range: {
                                startLineNumber: position.lineNumber,
                                endLineNumber: position.lineNumber,
                                startColumn: model.getPositionAt(startInCell).column,
                                endColumn: model.getPositionAt(endInCell).column,
                            },
                        }));

                    return { suggestions };
                }
            });
            disposables.push(provider);
        });

        return () => {
            disposables.forEach(d => d.dispose());
            cache.clear();
        };
    }, [monaco, requestCompletion, notebookRef]);
}
