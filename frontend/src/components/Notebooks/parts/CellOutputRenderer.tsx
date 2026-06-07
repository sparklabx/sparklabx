import React from 'react';
import DOMPurify from 'dompurify';
import { Sparkles } from 'lucide-react';
import { useAIContext } from '@/App';
import { Button } from '@/components/ui/button';
import { CellOutput } from '@/hooks/useJupyterKernel';
import { ansiToHtml, stripAnsi } from '../ansiToHtml';

// Malformed kernel output (corrupt HTML, weird JSON) can throw during render.
// Without a boundary the whole notebook unmounts; this contains the blast
// radius to a single cell and surfaces the error.
class CellOutputErrorBoundary extends React.Component<
    { children: React.ReactNode },
    { error: Error | null }
> {
    state = { error: null as Error | null };

    static getDerivedStateFromError(error: Error) {
        return { error };
    }

    componentDidCatch(error: Error, info: React.ErrorInfo) {
        // eslint-disable-next-line no-console
        console.error('CellOutputRenderer crashed:', error, info.componentStack);
    }

    render() {
        if (this.state.error) {
            return (
                <div className="border-t border-border bg-red-50 dark:bg-red-950/30 px-3 py-2 text-xs text-red-700 dark:text-red-300">
                    Failed to render cell output: {this.state.error.message}
                </div>
            );
        }
        return this.props.children;
    }
}

// Spark log/init line patterns to hide from main output
const SPARK_LOG_RE = /^\d{2}\/\d{2}\/\d{2}\s\d{2}:\d{2}:\d{2}\s(INFO|WARN|DEBUG|ERROR|TRACE)\s/;
const SPARK_INIT_RE = /^(Initializing Spark Session|Using Spark's default log4j|✅ Spark Session created!|✅ S3A Hadoop conf applied:|Lazy 'spark' variable defined|💡 To customize|👉 Then access)/;
const DELTA_S3A_NOISE_RE = /^(Id=null,?\s*S3AFileStatus\{|S3AFileStatus\{|.*isEmptyDirectory=.*|.*eTag=.*versionId=.*|java\.io\.FileNotFoundException: No such file or directory: s3a:\/\/)/;
const DELTA_INTERNALS_NOISE_RE = /(AddFile\(|StructField\(|StructType\(|MapType\(|ArrayType\(|Protocol\(|Metadata\(|nullCount":|minValues":|maxValues":|readerFeatures|writerFeatures|deletionVector|defaultRowCommitVersion|clusteringProvider)/;
const SCALA_DETAIL_LINE_RE = /^(import\s+[\w.{}*,\s]+|(?:val|var|lazy val)\s+\w+\s*[:=]|(?:res\d+|\w+)\s*:\s*[\w.[\](),{}<> ]+\s*=)/;

// Spark's df.show() prints ASCII tables ("+----+----+" separators and
// "|col|col|" data rows). Long rows can exceed 140 chars when columns hold
// emails/URLs, and we must NOT hide them inside the Details accordion.
const SPARK_TABLE_LINE_RE = /^(\+[-+]+\+|\|.*\|)\s*$/;

function isSparkTableLine(text: string): boolean {
    return SPARK_TABLE_LINE_RE.test(text.trim());
}

function isStructuredNoiseLine(text: string): boolean {
    const plain = text.trim();
    if (!plain) return false;
    // Spark show() output stays in user view regardless of length.
    if (isSparkTableLine(plain)) return false;
    if (plain.length >= 140) return true;

    const markerCount = [
        plain.includes(' : '),
        plain.includes('",'),
        plain.includes('":'),
        plain.includes('{'),
        plain.includes('}'),
        plain.includes('('),
        plain.includes(')'),
        plain.includes('['),
        plain.includes(']'),
    ].filter(Boolean).length;

    const indentation = /^\s{4,}/.test(text);
    const looksLikeSchemaFragment = markerCount >= 4 && (indentation || plain.includes('"type"') || plain.includes('"metadata"'));

    return looksLikeSchemaFragment;
}

function isScalaDetailText(text: string): boolean {
    const normalized = text.replace(/\r\n/g, '\n').trim();
    if (!normalized) return false;

    const lines = normalized.split('\n').map(line => line.trim()).filter(Boolean);
    if (lines.length === 0) return false;

    const matchedLines = lines.filter(line =>
        SCALA_DETAIL_LINE_RE.test(line) ||
        line === ')' ||
        line === '}' ||
        line === ']' ||
        /^[[\](){}]/.test(line)
    );

    return matchedLines.length > 0 && matchedLines.length >= Math.max(1, Math.ceil(lines.length * 0.6));
}

// Split stream text into user output, spark logs, and Scala REPL details.
function splitStreamOutput(text: string): { userLines: string; logLines: string; detailLines: string } {
    const lines = text.split('\n');
    const user: string[] = [];
    const logs: string[] = [];
    const details: string[] = [];
    for (const line of lines) {
        const plainLine = stripAnsi(line).trim();
        if (
            SPARK_LOG_RE.test(plainLine) ||
            SPARK_INIT_RE.test(plainLine) ||
            DELTA_S3A_NOISE_RE.test(plainLine) ||
            DELTA_INTERNALS_NOISE_RE.test(plainLine)
        ) {
            logs.push(line);
        } else if (isScalaDetailText(plainLine) || isStructuredNoiseLine(line)) {
            details.push(line);
        } else {
            user.push(line);
        }
    }
    return {
        userLines: user.join('\n'),
        logLines: logs.join('\n'),
        detailLines: details.join('\n'),
    };
}

const CellOutputRendererInner: React.FC<{ outputs: CellOutput[]; language?: string }> = ({ outputs, language }) => {
    const { sendPrompt } = useAIContext();
    const [showLogs, setShowLogs] = React.useState(false);
    const [showScalaDetails, setShowScalaDetails] = React.useState(false);
    if (!outputs || outputs.length === 0) return null;
    const detailLabel = language === 'scala'
        ? 'Scala Details'
        : language === 'python'
            ? 'Python Details'
            : 'Execution Details';

    // Collect all spark logs and identify if we have an error to merge streams
    let allLogLines = '';
    let allScalaDetails = '';
    const hasError = outputs.some(o => o.type === 'error');
    const consumedStreamIndices = new Set<number>();

    // If we have an error, we'll merge all preceding stream logs into it
    let mergedStreamText = '';
    if (hasError) {
        outputs.forEach((o, i) => {
            if (o.type === 'stream' && o.text) {
                // Check if this stream contains spark logs
                const { userLines, logLines, detailLines } = splitStreamOutput(o.text);
                if (logLines) allLogLines += (allLogLines ? '\n' : '') + logLines;
                if (detailLines) allScalaDetails += (allScalaDetails ? '\n' : '') + detailLines;

                if (userLines.trim()) {
                    mergedStreamText += (mergedStreamText ? '\n' : '') + userLines;
                    consumedStreamIndices.add(i);
                }
            }
        });
    }

    const processedOutputs = outputs.map((o, i) => {
        if (o.type === 'stream' && o.text) {
            if (consumedStreamIndices.has(i)) return null; // Will be shown in error box
            const { userLines, logLines, detailLines } = splitStreamOutput(o.text);
            if (logLines && !hasError) allLogLines += (allLogLines ? '\n' : '') + logLines;
            if (detailLines) allScalaDetails += (allScalaDetails ? '\n' : '') + detailLines;
            return { ...o, text: userLines };
        }
        if (o.type === 'result' && o.data?.['text/plain']) {
            const text = o.data['text/plain'] as string;
            if (isScalaDetailText(stripAnsi(text))) {
                allScalaDetails += (allScalaDetails ? '\n\n' : '') + text;
                return null;
            }
        }
        return o;
    }).filter(o => o !== null) as CellOutput[];

    const renderOutput = (output: CellOutput, idx: number) => {
        const isLast = idx === processedOutputs.length - 1;
        const mbClass = isLast ? '' : 'mb-1';

        // Stream output (stdout/stderr) — spark logs already filtered out
        if (output.type === 'stream') {
            if (!output.text?.trim()) return null;
            return (
                <pre
                    key={idx}
                    className={`whitespace-pre-wrap text-foreground ${mbClass}`}
                    dangerouslySetInnerHTML={{ __html: ansiToHtml(output.text || '') }}
                />
            );
        }

        // Result output
        if (output.type === 'result') {
            const data = output.data;

            // HTML output
            if (data?.['text/html']) {
                return (
                    <div
                        key={idx}
                        className={`overflow-auto max-h-96 ${mbClass}`}
                        dangerouslySetInnerHTML={{ __html: DOMPurify.sanitize(data['text/html'] as string) }}
                    />
                );
            }

            // Image output (PNG, JPEG, etc.)
            if (data?.['image/png']) {
                return (
                    <img
                        key={idx}
                        src={`data:image/png;base64,${data['image/png']}`}
                        alt="Output"
                        className={`max-w-full h-auto ${mbClass}`}
                    />
                );
            }
            if (data?.['image/jpeg']) {
                return (
                    <img
                        key={idx}
                        src={`data:image/jpeg;base64,${data['image/jpeg']}`}
                        alt="Output"
                        className={`max-w-full h-auto ${mbClass}`}
                    />
                );
            }

            // Plain text output
            if (data?.['text/plain']) {
                const text = data['text/plain'] as string;
                // Try to detect if it's a table (contains | or +---)
                if (text.includes('|') || text.includes('+---')) {
                    return (
                        <pre
                            key={idx}
                            className={`whitespace-pre overflow-auto text-xs bg-background p-2 rounded border ${mbClass}`}
                            dangerouslySetInnerHTML={{ __html: ansiToHtml(text) }}
                        />
                    );
                }
                return (
                    <pre
                        key={idx}
                        className={`whitespace-pre-wrap text-emerald-600 dark:text-emerald-400 ${mbClass}`}
                        dangerouslySetInnerHTML={{ __html: ansiToHtml(text) }}
                    />
                );
            }

            // Fallback: JSON
            return (
                <pre key={idx} className={`whitespace-pre-wrap text-xs bg-background p-2 rounded border overflow-auto ${mbClass}`}>
                    {JSON.stringify(data, null, 2)}
                </pre>
            );
        }

        // Error output
        if (output.type === 'error') {
            const handleDebug = () => {
                const traceback = output.traceback ? output.traceback.join('\n') : '';
                const prompt = `I encountered an error in my notebook:
Error: ${output.ename}: ${output.evalue}

Traceback:
${traceback}

Please explain this error and suggest a fix.`;
                sendPrompt(prompt);
            };

            return (
                <div key={idx} className={`text-red-600 dark:text-red-400 p-2 bg-red-50 dark:bg-red-950/30 rounded border border-red-200 dark:border-red-800 overflow-x-auto ${mbClass}`}>
                    <div className="font-semibold flex items-center justify-between gap-2 mb-1">
                        <div className="flex items-center gap-2">
                            <svg className="h-4 w-4" fill="currentColor" viewBox="0 0 20 20">
                                <path fillRule="evenodd" d="M10 18a8 8 0 100-16 8 8 0 000 16zM8.707 7.293a1 1 0 00-1.414 1.414L8.586 10l-1.293 1.293a1 1 0 101.414 1.414L10 11.414l1.293 1.293a1 1 0 001.414-1.414L11.414 10l1.293-1.293a1 1 0 00-1.414-1.414L10 8.586 8.707 7.293z" clipRule="evenodd" />
                            </svg>
                            <div className="whitespace-pre-wrap">{output.ename}: {output.evalue}</div>
                        </div>
                        <Button
                            variant="ghost"
                            size="sm"
                            onClick={handleDebug}
                            className="h-6 px-2 text-xs hover:bg-red-200 dark:hover:bg-red-900/50 text-red-700 dark:text-red-300 gap-1"
                        >
                            <Sparkles className="h-3 w-3" />
                            Debug
                        </Button>
                    </div>
                    {(mergedStreamText || (output.traceback && output.traceback.length > 0)) && (
                        <pre
                            className="mt-2 text-xs whitespace-pre-wrap break-words font-mono opacity-90 overflow-x-auto max-w-full border-t border-red-200 dark:border-red-800/50 pt-2"
                        >
                            {mergedStreamText && (
                                <div
                                    className="mb-2 pb-2 border-b border-red-200 dark:border-red-800/30"
                                    dangerouslySetInnerHTML={{ __html: ansiToHtml(mergedStreamText) }}
                                />
                            )}
                            {output.traceback && (
                                <div dangerouslySetInnerHTML={{ __html: ansiToHtml(output.traceback.join('\n')) }} />
                            )}
                        </pre>
                    )}
                </div>
            );
        }

        return null;
    };

    return (
        <div className="border-t border-border bg-muted/30 px-3 py-2 text-sm">
            {processedOutputs.map((output, idx) => renderOutput(output, idx))}
            {allLogLines && (
                <div className="mt-1">
                    <button
                        onClick={() => setShowLogs(!showLogs)}
                        className="text-[10px] text-muted-foreground hover:text-foreground flex items-center gap-1"
                    >
                        <span className={`transition-transform ${showLogs ? 'rotate-90' : ''}`}>&#9654;</span>
                        Spark Logs ({allLogLines.split('\n').length} lines)
                    </button>
                    {showLogs && (
                        <pre className="mt-1 text-[10px] text-muted-foreground whitespace-pre-wrap max-h-48 overflow-auto bg-background/50 rounded p-2 border">
                            {allLogLines}
                        </pre>
                    )}
                </div>
            )}
            {allScalaDetails && (
                <div className="mt-1">
                    <button
                        onClick={() => setShowScalaDetails(!showScalaDetails)}
                        className="text-[10px] text-muted-foreground hover:text-foreground flex items-center gap-1"
                    >
                        <span className={`transition-transform ${showScalaDetails ? 'rotate-90' : ''}`}>&#9654;</span>
                        {detailLabel} ({allScalaDetails.split('\n').length} lines)
                    </button>
                    {showScalaDetails && (
                        <pre
                            className="mt-1 text-[10px] whitespace-pre-wrap max-h-56 overflow-auto bg-background/50 rounded p-2 border"
                            dangerouslySetInnerHTML={{ __html: ansiToHtml(allScalaDetails) }}
                        />
                    )}
                </div>
            )}
        </div>
    );
};

const MemoInner = React.memo(CellOutputRendererInner);

export const CellOutputRenderer: React.FC<{ outputs: CellOutput[]; language?: string }> = (props) => (
    <CellOutputErrorBoundary>
        <MemoInner {...props} />
    </CellOutputErrorBoundary>
);
