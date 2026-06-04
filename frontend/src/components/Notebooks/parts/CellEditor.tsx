import React, { useState, useEffect, useRef } from 'react';
import Editor from '@monaco-editor/react';
import MarkdownPreview from '@uiw/react-markdown-preview';
import {
    Play,
    Loader2,
    Clock,
    ChevronUp,
    ChevronDown,
    Trash2,
    Eraser,
    CheckCircle2,
    XCircle,
} from 'lucide-react';
import { Button } from '@/components/ui/button';
import { Badge } from '@/components/ui/badge';
import { useTheme } from '@/components/theme-provider';
import { NotebookCell } from '@/hooks/useNotebook';
import { CellOutput } from '@/hooks/useJupyterKernel';
import { CellOutputRenderer } from './CellOutputRenderer';

// Map from Monaco model → cell id, populated by CellEditor.onMount.
// Used by the shared completion/hover providers to find which cell a model
// belongs to so we can prepend preceding cells' source for static analysis.
export const cellModelMap = new WeakMap<any, string>();

interface CellEditorProps {
    cell: NotebookCell;
    isRunning: boolean;
    isPending?: boolean;  // Cell is queued for execution (Run All)
    kernelBusy?: boolean;  // Kernel unavailable (e.g. Spark booting) — disable Play silently
    executionCount?: number;  // In [N]:
    hasExecuted: boolean;
    output?: CellOutput[];
    language: string;
    readOnly?: boolean;
    onUpdate: (source: string) => void;
    onRun: (sourceOverride?: string) => void;
    onClearOutput: () => void;
    onDelete: () => void;
    onMoveUp: () => void;
    onMoveDown: () => void;
}

export const CellEditor: React.FC<CellEditorProps> = React.memo(({
    cell,
    isRunning,
    readOnly,
    isPending,
    kernelBusy,
    executionCount,
    hasExecuted,
    output,
    language,
    onUpdate,
    onRun,
    onClearOutput,
    onDelete,
    onMoveUp,
    onMoveDown,
}) => {
    const { theme } = useTheme();
    // Calculate resolved theme (system -> actual value)
    const resolvedTheme = theme === 'system'
        ? (window.matchMedia('(prefers-color-scheme: dark)').matches ? 'dark' : 'light')
        : theme;
    const [isEditing, setIsEditing] = useState(() => {
        // Code cells always in edit mode
        if (cell.type === 'code') return true;
        // Markdown cells: start in edit mode if new (temp ID) or empty
        if (cell.type === 'markdown') {
            const isNewCell = cell.id.startsWith('temp-') || !cell.source?.trim();
            return isNewCell;
        }
        return false;
    });

    const monacoLanguage = language === 'scala' ? 'scala' : language === 'sql' ? 'sql' : 'python';
    const isMarkdown = cell.type === 'markdown';

    // Local source state to prevent cursor jumping on parent re-render
    const [localSource, setLocalSource] = useState(cell.source || '');
    const prevCellIdRef = useRef(cell.id);
    useEffect(() => {
        if (cell.id !== prevCellIdRef.current) {
            setLocalSource(cell.source || '');
            prevCellIdRef.current = cell.id;
        }
    }, [cell.id, cell.source]);

    // Refs to track latest callbacks for keybinding (avoids stale closure)
    const onRunRef = useRef(onRun);
    const onUpdateRef = useRef(onUpdate);

    // Keep refs up to date
    useEffect(() => {
        onRunRef.current = onRun;
        onUpdateRef.current = onUpdate;
    }, [onRun, onUpdate]);

    return (
        <div
            className={`rounded-lg overflow-hidden mb-1 transition-all duration-200 group/cell ${isMarkdown
                ? `border ${isEditing ? 'border-border' : 'border-transparent hover:border-border'}`
                : 'border border-border'
                }`}
        >
            {/* Cell toolbar */}
            <div
                className={`flex items-center justify-between px-3 py-1 bg-muted/50 border-b border-border transition-opacity duration-200 ${isMarkdown && !isEditing ? 'opacity-0 group-hover/cell:opacity-100' : 'opacity-100'
                    }`}
            >
                <div className="flex items-center gap-2 h-5">
                    <Badge variant="outline" className="text-xs shrink-0">
                        {cell.type === 'code' ? monacoLanguage : 'markdown'}
                    </Badge>
                    {cell.type === 'code' && (
                        <span className="text-[10px] font-mono text-muted-foreground whitespace-nowrap shrink-0 translate-y-[1px]">
                            {isRunning ? 'In [*]:' : executionCount ? `In [${executionCount}]:` : 'In [ ]:'}
                        </span>
                    )}
                    {isPending && cell.type === 'code' && (
                        <div className="flex items-center gap-1 text-amber-500 shrink-0">
                            <Clock className="h-3.5 w-3.5" />
                            <span className="text-xs">Queued</span>
                        </div>
                    )}
                    {isRunning && cell.type === 'code' && (
                        <div className="flex items-center gap-1 text-blue-500 shrink-0">
                            <Loader2 className="h-3.5 w-3.5 animate-spin" />
                            <span className="text-xs">Running</span>
                        </div>
                    )}
                    {hasExecuted && cell.type === 'code' && !isRunning && !isPending && (
                        <div className={`flex items-center gap-1 shrink-0 ${output?.some(o => o.type === 'error') ? 'text-destructive' : 'text-emerald-600'}`}>
                            {output?.some(o => o.type === 'error') ? (
                                <XCircle className="h-3.5 w-3.5" />
                            ) : (
                                <CheckCircle2 className="h-3.5 w-3.5" />
                            )}
                        </div>
                    )}
                    {cell.executionTime && (
                        <span className="text-xs text-muted-foreground">
                            {(cell.executionTime / 1000).toFixed(2)}s
                        </span>
                    )}
                </div>
                <div className="flex items-center gap-1">
                    {cell.type === 'code' && (
                        <>
                            <Button
                                variant="ghost"
                                size="icon"
                                className="h-6 w-6"
                                onClick={() => onRun()}
                                disabled={isRunning || isPending || readOnly || kernelBusy}
                            >
                                {isRunning ? (
                                    <Loader2 className="h-4 w-4 animate-spin" />
                                ) : isPending ? (
                                    <Clock className="h-4 w-4 text-amber-500" />
                                ) : (
                                    <Play className="h-4 w-4" />
                                )}
                            </Button>
                            {(output && output.length > 0) && (
                                <Button
                                    variant="ghost"
                                    size="icon"
                                    className="h-6 w-6 text-muted-foreground hover:text-foreground"
                                    onClick={onClearOutput}
                                    disabled={readOnly}
                                    title="Clear output"
                                >
                                    <Eraser className="h-4 w-4" />
                                </Button>
                            )}
                        </>
                    )}
                    <Button variant="ghost" size="icon" className="h-6 w-6" onClick={onMoveUp} disabled={readOnly}>
                        <ChevronUp className="h-4 w-4" />
                    </Button>
                    <Button variant="ghost" size="icon" className="h-6 w-6" onClick={onMoveDown} disabled={readOnly}>
                        <ChevronDown className="h-4 w-4" />
                    </Button>
                    <Button variant="ghost" size="icon" className="h-6 w-6 text-destructive" onClick={onDelete} disabled={readOnly}>
                        <Trash2 className="h-4 w-4" />
                    </Button>
                </div>
            </div>

            {/* Cell content */}
            {cell.type === 'code' ? (
                <div>
                    <Editor
                        // Precise height calculation with BUFFER to prevent scroll-jumping
                        // We add extra buffer (40px) so when user hits Enter, the new line is already "visible"
                        // in terms of container size, preventing Monaco from scrolling the top line out of view.
                        height={`${Math.max(60, localSource.split('\n').length * 20 + 40)}px`}
                        language={monacoLanguage} // Dynamic language support (python, scala, sql)
                        value={localSource}
                        onChange={readOnly ? undefined : (value) => { setLocalSource(value || ''); onUpdate(value || ''); }}
                        theme={resolvedTheme === 'dark' ? 'vs-dark' : 'light'}
                        options={{
                            readOnly: !!readOnly,
                            minimap: { enabled: false },
                            fontSize: 13,
                            lineHeight: 20, // Fixed line height
                            lineNumbers: 'on',
                            scrollBeyondLastLine: false,
                            automaticLayout: true, // Re-enable to prevent blank screen
                            renderLineHighlight: 'none',
                            fixedOverflowWidgets: true,
                            scrollbar: {
                                alwaysConsumeMouseWheel: false,
                                vertical: 'hidden',
                                horizontal: 'auto',
                                handleMouseWheel: false, // Prevent mouse wheel scrolling since we auto-expand
                            },
                            overviewRulerLanes: 0,
                            hideCursorInOverviewRuler: true,
                            padding: {
                                top: 10,
                                bottom: 10,
                            },
                            // Enhanced autocomplete and IntelliSense
                            suggest: {
                                showKeywords: true,
                                showSnippets: true,
                                showFunctions: true,
                                showVariables: true,
                                showClasses: true,
                                showModules: true,
                                showConstants: true,
                                showProperties: true,
                                showMethods: true,
                                showValues: true,
                                showWords: true,
                                filterGraceful: true, // Allow fuzzy filtering
                            },
                            quickSuggestions: {
                                other: true,
                                comments: false,
                                strings: true,
                            },
                            quickSuggestionsDelay: 10, // Make suggestions appear instantly
                            suggestOnTriggerCharacters: true,
                            acceptSuggestionOnEnter: 'on',
                            tabCompletion: 'on',
                            wordBasedSuggestions: 'matchingDocuments',
                            parameterHints: {
                                enabled: true,
                                cycle: true,
                            },
                        }}
                        onMount={(editor, monaco) => {
                            // Register this model so completion/hover providers can find the cell.
                            const model = editor.getModel();
                            if (model) cellModelMap.set(model, cell.id);

                            // Add Ctrl/Cmd + Enter to run cell
                            editor.addAction({
                                id: 'run-cell',
                                label: 'Run Cell',
                                keybindings: [
                                    monaco.KeyMod.CtrlCmd | monaco.KeyCode.Enter,
                                ],
                                run: () => {
                                    // Get current value from editor
                                    const currentValue = editor.getValue();

                                    // Use refs to avoid stale closure - always calls latest callbacks
                                    onUpdateRef.current(currentValue);
                                    onRunRef.current(currentValue);
                                },
                            });
                        }}
                    />
                </div>
            ) : isEditing ? (
                <div className="w-full">
                    <textarea
                        ref={(el) => {
                            if (el) {
                                el.style.height = 'auto';
                                el.style.height = `${el.scrollHeight}px`;
                            }
                        }}
                        className="w-full px-3 py-2 bg-transparent resize-none outline-none text-foreground font-mono text-sm border-none focus:ring-0"
                        value={cell.source || ''}
                        onChange={(e) => {
                            onUpdate(e.target.value);
                            e.target.style.height = 'auto';
                            e.target.style.height = `${e.target.scrollHeight}px`;
                        }}
                        onBlur={() => setIsEditing(false)}
                        autoFocus
                        placeholder="Type markdown..."
                        style={{ minHeight: '60px' }}
                    />
                </div>
            ) : (
                <div
                    className="p-4 cursor-pointer hover:bg-muted/20 min-h-[60px]"
                    onClick={() => setIsEditing(true)}
                >
                    {cell.source ? (
                        <MarkdownPreview
                            source={cell.source}
                            style={{ background: 'transparent', color: 'inherit' }}
                            wrapperElement={{ 'data-color-mode': resolvedTheme === 'dark' ? 'dark' : 'light' }}
                        />
                    ) : (
                        <span className="text-muted-foreground italic text-sm">Double click to edit...</span>
                    )}
                </div>
            )}

            {/* Cell output - show placeholder when running to prevent layout jump */}
            {isRunning && (!output || output.length === 0) && cell.type === 'code' && (
                <div className="px-4 py-2 text-muted-foreground text-sm flex items-center gap-2">
                    <Loader2 className="h-4 w-4 animate-spin" />
                    <span>Running...</span>
                </div>
            )}
            {output && <CellOutputRenderer outputs={output} language={language} />}
        </div>
    );
}, (prevProps, nextProps) => {
    // Note: We MUST include cell.id here.
    // Even though we prevent unmounting via stable key (_frontendId),
    // we need to re-render when ID changes (temp -> real) so that:
    // 1. The `cell` prop updates to the version with the real ID.
    // 2. The `onDelete` closure updates to use the real ID.
    // Use stable key prevents the "flash" (unmount), this just updates props.
    return (
        prevProps.cell.id === nextProps.cell.id &&
        prevProps.cell.source === nextProps.cell.source &&
        prevProps.cell.type === nextProps.cell.type &&
        prevProps.isRunning === nextProps.isRunning &&
        prevProps.isPending === nextProps.isPending &&
        prevProps.kernelBusy === nextProps.kernelBusy &&
        prevProps.hasExecuted === nextProps.hasExecuted &&
        prevProps.output === nextProps.output &&
        prevProps.language === nextProps.language &&
        prevProps.readOnly === nextProps.readOnly
    );
});
