import { useEffect, useRef, MutableRefObject } from 'react';
import { NotebookCell } from '@/hooks/useNotebook';
import { cellModelMap } from '@/components/Notebooks/parts/CellEditor';

const CACHE_EXPIRY_MS = 5 * 60 * 1000;

interface NotebookRefValue { cells?: NotebookCell[] | any[] }

type RequestInspectionFn = (code: string, cursorPos: number, detailLevel?: number) => Promise<any>;

export function useKernelHoverProvider(
    monaco: any,
    requestInspection: RequestInspectionFn | undefined,
    notebookRef: MutableRefObject<NotebookRefValue | null>,
) {
    const inspectionCacheRef = useRef(new Map<string, { result: any; timestamp: number }>());
    const inFlightRef = useRef(new Map<string, Promise<any>>());
    const lastInspectAtRef = useRef(new Map<string, number>()); // lang -> ts

    useEffect(() => {
        if (!monaco || !requestInspection) return;

        // Prefer text/markdown → text/plain (in code block) → stripped HTML.
        // Keeps rich formatting where the kernel supplies it; falls back gracefully.
        const formatHover = (result: any, model: any, position: any) => {
            if (!result || result.status === 'error') return null;
            if (!result?.data) return null;
            const data = result.data as Record<string, string>;

            // eslint-disable-next-line no-control-regex
            const stripAnsi = (s: string) => s.replace(/\u001b\[[0-9;]*m/g, '');

            let body: string | null = null;
            if (data['text/markdown']?.trim()) {
                body = stripAnsi(data['text/markdown']).trim();
            } else if (data['text/plain']?.trim()) {
                body = '```text\n' + stripAnsi(data['text/plain']).trim() + '\n```';
            } else if (data['text/html']?.trim()) {
                const stripped = data['text/html']
                    .replace(/<br\s*\/?>/gi, '\n')
                    .replace(/<p>/gi, '\n')
                    .replace(/<[^>]+>/g, '')
                    .trim();
                if (stripped) body = '```text\n' + stripAnsi(stripped) + '\n```';
            }
            if (!body) return null;
            if (body.trim() === '<error>' || body.trim() === '&lt;error&gt;' || body.trim() === '```text\n<error>\n```') return null;

            const word = model.getWordAtPosition(position);
            return {
                range: new monaco.Range(
                    position.lineNumber,
                    word?.startColumn || position.column,
                    position.lineNumber,
                    word?.endColumn || position.column,
                ),
                contents: [{ value: body }],
            };
        };

        const disposables = ['python', 'scala'].map(lang =>
            monaco.languages.registerHoverProvider(lang, {
                provideHover: async (model: any, position: any, token: any) => {
                    // Prepend preceding cells so the kernel can resolve symbols imported upstream
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
                    const fullCode = prefix + model.getValue();
                    const fullOffset = prefix.length + model.getOffsetAt(position);
                    const cacheKey = `${lang}@${prefix.length}@${fullCode.length}@${fullOffset}`;

                    // Check cache
                    const cached = inspectionCacheRef.current.get(cacheKey);
                    if (cached && (Date.now() - cached.timestamp) < CACHE_EXPIRY_MS) {
                        return formatHover(cached.result, model, position);
                    }

                    // Scala presentation compiler inspection can crash if spammed concurrently.
                    // Throttle + single-flight per cacheKey.
                    const now = Date.now();
                    const lastAt = lastInspectAtRef.current.get(lang) || 0;
                    if (lang === 'scala' && (now - lastAt) < 250) return null;
                    lastInspectAtRef.current.set(lang, now);

                    try {
                        let p = inFlightRef.current.get(cacheKey);
                        if (!p) {
                            p = requestInspection(fullCode, fullOffset, 0).finally(() => {
                                inFlightRef.current.delete(cacheKey);
                            });
                            inFlightRef.current.set(cacheKey, p);
                        }
                        const result = await p;
                        if (token.isCancellationRequested) return null;
                        if (result?.status === 'ok' && result?.data && (result.data['text/markdown'] || result.data['text/plain'] || result.data['text/html'])) {
                            inspectionCacheRef.current.set(cacheKey, { result, timestamp: Date.now() });
                            if (inspectionCacheRef.current.size > 500) {
                                const now = Date.now();
                                for (const [k, v] of inspectionCacheRef.current) {
                                    if (now - v.timestamp > CACHE_EXPIRY_MS) inspectionCacheRef.current.delete(k);
                                }
                            }
                        }
                        return formatHover(result, model, position);
                    } catch {
                        return null;
                    }
                }
            })
        );

        return () => {
            disposables.forEach(d => d.dispose());
        };
    }, [monaco, requestInspection, notebookRef]);
}
