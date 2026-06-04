/**
 * useNotebook Hook
 * React hook for notebook management with Backend API
 */
import { useState, useCallback, useEffect, useRef, useMemo } from 'react';
import authService from '@/services/authService';
import { devLog } from '@/lib/debug';
import {
    NotebookDTO,
    NotebookDetailDTO,
    NotebookCellDTO,
    ClusterConfig,
    NotebookLanguage,
    CellType,
    WebSocketMessage,
    notebookService,
} from '@/services/notebookService';

// ============ Global Notebook Cache (LRU, max 20 entries) ============
const CACHE_MAX = 20;
const cacheOrder: string[] = []; // track access order for LRU eviction
export const notebookCache = new Map<string, NotebookDetailDTO>();

function cacheSet(id: string, data: NotebookDetailDTO) {
    // Move to end (most recently used)
    const idx = cacheOrder.indexOf(id);
    if (idx >= 0) cacheOrder.splice(idx, 1);
    cacheOrder.push(id);
    notebookCache.set(id, data);
    // Evict oldest if over limit
    while (cacheOrder.length > CACHE_MAX) {
        const oldest = cacheOrder.shift()!;
        notebookCache.delete(oldest);
    }
}
// Track notebooks with pending local changes (reorder, add, delete) to prevent stale overwrites
export const localDirty = new Set<string>();

// ============ Types ============

export type ConnectionStatus = 'disconnected' | 'connecting' | 'connected' | 'error';

export interface CellOutput {
    type: 'stream' | 'result' | 'error';
    text?: string;
    data?: Record<string, unknown>;
    ename?: string;
    evalue?: string;
    traceback?: string[];
}

export interface NotebookCell {
    id: string;
    type: CellType;
    source: string;
    order: number;
    output?: CellOutput[];
    isRunning?: boolean;
    executionTime?: number;
    last_output?: { outputs?: CellOutput[]; executed?: boolean };
    _frontendId?: string;
}

// ============ Hook: useNotebook ============

export function useNotebook(notebookId?: string) {
    const [notebook, setNotebook] = useState<NotebookDetailDTO | null>(null);
    // Initialize loading to true if notebookId is provided to prevent flash of "Not found"
    const [loading, setLoading] = useState(!!notebookId);
    const [error, setError] = useState<string | null>(null);

    // Load notebook
    const loadNotebook = useCallback(async (id: string) => {
        // If notebook has pending local changes, use cache to avoid overwriting
        if (localDirty.has(id)) {
            const cached = notebookCache.get(id);
            if (cached) {
                setNotebook(cached);
                setLoading(false);
                return;
            }
        }

        // Check cache first
        const cached = notebookCache.get(id);
        if (cached) {
            setNotebook(cached);
            setLoading(false);
            return;
        }

        // No cache - show loading and fetch
        setLoading(true);
        setError(null);
        try {
            const data = await notebookService.getNotebook(id);
            if (data) {
                // Apply pending reorder from localStorage if API call was lost during reload
                const savedOrder = localStorage.getItem(`notebook-order-${id}`);
                if (savedOrder && data.cells) {
                    try {
                        const orderIds: string[] = JSON.parse(savedOrder);
                        // Check if API already has correct order (reorder API succeeded)
                        const apiOrder = data.cells
                            .slice().sort((a, b) => a.order - b.order)
                            .map(c => c.id);
                        const orderMatches = orderIds.length === apiOrder.length &&
                            orderIds.every((id, i) => id === apiOrder[i]);

                        if (orderMatches) {
                            // API order matches localStorage — reorder was persisted, clean up
                            devLog('[loadNotebook] API order matches localStorage, cleaning up');
                            localStorage.removeItem(`notebook-order-${id}`);
                        } else {
                            // API order differs — apply localStorage order and retry
                            devLog('[loadNotebook] Applying localStorage order (API stale)');
                            data.cells = data.cells.map(c => ({
                                ...c,
                                order: orderIds.indexOf(c.id) >= 0 ? orderIds.indexOf(c.id) : c.order,
                            }));
                            notebookService.reorderCells(id, orderIds)
                                .then(() => localStorage.removeItem(`notebook-order-${id}`))
                                .catch(() => {});
                        }
                    } catch { localStorage.removeItem(`notebook-order-${id}`); }
                }
                cacheSet(id, data);
                setNotebook(data);
            }
        } catch (e) {
            setError('Failed to load notebook');
        } finally {
            setLoading(false);
        }
    }, []);

    // Auto-load when notebookId changes
    useEffect(() => {
        if (notebookId) {
            loadNotebook(notebookId);
        }
    }, [notebookId, loadNotebook]);

    // Sync cache when notebook changes
    useEffect(() => {
        if (notebook) {
            cacheSet(notebook.id, notebook);
        }
    }, [notebook]);

    // Cell operations
    const addCell = useCallback(async (type: CellType, source = '', afterOrder?: number) => {
        if (!notebook) return null;

        // Generate temporary ID for immediate UI update
        const tempId = `temp-${Date.now()}-${Math.random().toString(36).substr(2, 9)}`;
        const insertOrder = afterOrder !== undefined ? afterOrder + 1 : notebook.cells.length;

        // Optimistic update: add cell to UI immediately (before API call)
        const tempCell = {
            id: tempId,
            type,
            source,
            order: insertOrder,
            notebook_id: notebook.id,
            created_at: new Date().toISOString(),
            updated_at: new Date().toISOString(),
            _frontendId: tempId, // Stable ID
        };

        setNotebook(prev => {
            if (!prev) return null;
            // Shift cells that come after the insertion point
            const updatedCells = prev.cells.map(c =>
                c.order >= insertOrder ? { ...c, order: c.order + 1 } : c
            );
            const newNotebook = {
                ...prev,
                cells: [...updatedCells, tempCell] as any
            };
            cacheSet(prev.id, newNotebook);
            return newNotebook;
        });

        // Sync with backend in background (fire-and-forget style, but update ID when done)
        notebookService.addCell(notebook.id, { type, source }, afterOrder)
            .then(realCell => {
                if (realCell) {
                    setNotebook(prev => {
                        if (!prev) return null;

                        // Check if the temp cell still exists in the local state
                        // If user deleted it while request was pending, it won't be here
                        const cellExists = prev.cells.some(c => c.id === tempId);

                        if (!cellExists) {
                            devLog('[useNotebook] Cell was deleted by user while adding. Cleaning up backend:', realCell.id);
                            notebookService.deleteCell(notebook.id, realCell.id as string).catch(console.error);
                            return prev;
                        }

                        // Replace temp cell with real cell from backend but PRESERVE _frontendId
                        const newNotebook = {
                            ...prev,
                            cells: prev.cells.map(c =>
                                c.id === tempId ? { ...realCell, order: c.order, _frontendId: tempId } : c
                            ) as any
                        };
                        cacheSet(prev.id, newNotebook);
                        return newNotebook;
                    });
                }
            })
            .catch(error => {
                console.error('[useNotebook] Failed to add cell:', error);
                // Rollback: remove temp cell on error
                setNotebook(prev => {
                    if (!prev) return null;
                    return {
                        ...prev,
                        cells: prev.cells.filter(c => c.id !== tempId)
                    };
                });
            });

        return tempCell;
    }, [notebook]);

    const updateCell = useCallback(async (cellId: string, updates: Partial<{ type: CellType; source: string; order: number; last_output: any; last_execution_time_ms: number; execution_count: number }>) => {
        if (!notebook || !cellId || cellId === 'undefined') return null;
        const isSourceOnly = updates.source !== undefined && Object.keys(updates).length === 1;
        const cell = await notebookService.updateCell(notebook.id, cellId, updates);
        if (cell && !isSourceOnly) {
            // Only update notebook state for non-source changes (output, type, order)
            // Source-only updates are already reflected in the editor — updating would cause cursor jump
            setNotebook(prev => {
                if (!prev) return null;
                return {
                    ...prev,
                    // Only merge content fields from response — never override source or order
                    cells: prev.cells.map(c => c.id === cellId ? {
                        ...c,
                        type: cell.type ?? c.type,
                        last_output: cell.last_output ?? c.last_output,
                        last_execution_time_ms: cell.last_execution_time_ms ?? c.last_execution_time_ms,
                        execution_count: cell.execution_count ?? c.execution_count,
                        updated_at: cell.updated_at ?? c.updated_at,
                    } : c)
                };
            });
        }
        return cell;
    }, [notebook]);

    const deleteCell = useCallback(async (cellId: string) => {
        if (!notebook) return false;

        // Optimistic update: remove from UI immediately
        setNotebook(prev => {
            if (!prev) return null;
            const newCells = prev.cells.filter(c => c.id !== cellId);
            devLog(`[useNotebook] Deleting cell ${cellId}. Count: ${prev.cells.length} -> ${newCells.length}`);

            const newNotebook = {
                ...prev,
                cells: newCells
            };
            cacheSet(prev.id, newNotebook); // Sync cache
            return newNotebook;
        });

        // If it's a temporary cell, we don't need to call backend
        if (cellId.startsWith('temp-')) {
            devLog('[useNotebook] Deleting local temp cell:', cellId);
            return true;
        }

        // For real cells, sync with backend
        try {
            await notebookService.deleteCell(notebook.id, cellId);
            return true;
        } catch (error) {
            console.error('[useNotebook] Failed to delete cell:', error);
            // Rollback on error
            notebookService.getNotebook(notebook.id).then(data => {
                if (data) setNotebook(data);
            });
            return false;
        }
    }, [notebook]);

    const updateNotebook = useCallback(async (updates: Partial<NotebookDTO>) => {
        if (!notebook) return null;
        const updated = await notebookService.updateNotebook(notebook.id, updates);
        if (updated) {
            setNotebook(prev => {
                if (!prev) return null;
                // Preserve cells array when updating notebook metadata
                return {
                    ...prev,
                    ...updated,
                    cells: prev.cells // Keep existing cells
                };
            });
        }
        return updated;
    }, [notebook]);

    return {
        notebook,
        loading,
        error,
        loadNotebook,
        addCell,
        updateCell,
        deleteCell,
        updateNotebook,
        setNotebook,
    };
}

// ============ Hook: useNotebookExecution ============

export function useNotebookExecution(notebookId: string | undefined) {
    // Get token from authService instead of context
    const token = useMemo(() => authService.getToken(), []);
    const [connectionStatus, setConnectionStatus] = useState<ConnectionStatus>('disconnected');
    const [cellOutputs, setCellOutputs] = useState<Record<string, CellOutput[]>>({});
    const [runningCells, setRunningCells] = useState<Set<string>>(new Set());

    const wsRef = useRef<WebSocket | null>(null);
    const pendingExecutions = useRef<Map<string, { cellId: string; startTime: number }>>(new Map());

    // Handle WebSocket messages
    const handleMessage = useCallback((msg: WebSocketMessage) => {
        const msgType = msg.header.msg_type;
        const parentMsgId = msg.parent_header?.msg_id;

        // Find which cell this message is for
        const execution = parentMsgId ? pendingExecutions.current.get(parentMsgId) : null;
        const cellId = execution?.cellId;

        if (!cellId) return;

        switch (msgType) {
            case 'stream':
                // stdout/stderr
                setCellOutputs(prev => ({
                    ...prev,
                    [cellId]: [
                        ...(prev[cellId] || []),
                        { type: 'stream', text: msg.content.text as string }
                    ]
                }));
                break;

            case 'execute_result':
            case 'display_data':
                // Result data
                setCellOutputs(prev => ({
                    ...prev,
                    [cellId]: [
                        ...(prev[cellId] || []),
                        { type: 'result', data: msg.content.data as Record<string, unknown> }
                    ]
                }));
                break;

            case 'error':
                // Error
                setCellOutputs(prev => ({
                    ...prev,
                    [cellId]: [
                        ...(prev[cellId] || []),
                        {
                            type: 'error',
                            ename: msg.content.ename as string,
                            evalue: msg.content.evalue as string,
                            traceback: msg.content.traceback as string[]
                        }
                    ]
                }));
                setRunningCells(prev => {
                    const next = new Set(prev);
                    next.delete(cellId);
                    return next;
                });
                if (parentMsgId) {
                    pendingExecutions.current.delete(parentMsgId);
                }
                break;

            case 'status':
                // Kernel status
                if (msg.content.execution_state === 'idle' && parentMsgId) {
                    // Execution finished
                    setRunningCells(prev => {
                        const next = new Set(prev);
                        next.delete(cellId);
                        return next;
                    });
                    pendingExecutions.current.delete(parentMsgId);
                }
                break;
        }
    }, []);

    // Connect to WebSocket
    const connect = useCallback(() => {
        if (!notebookId || !token) return;

        setConnectionStatus('connecting');

        const ws = notebookService.connectNotebookWebSocket(notebookId, token, {
            onMessage: handleMessage,
            onOpen: () => setConnectionStatus('connected'),
            onClose: () => setConnectionStatus('disconnected'),
            onError: () => setConnectionStatus('error'),
        });

        wsRef.current = ws;
    }, [notebookId, token, handleMessage]);

    // Disconnect
    const disconnect = useCallback(() => {
        wsRef.current?.close();
        wsRef.current = null;
        setConnectionStatus('disconnected');
    }, []);

    // Execute cell with policy check
    const executeCell = useCallback(async (cellId: string, code: string) => {
        if (!wsRef.current || connectionStatus !== 'connected') {
            console.error('Not connected to kernel');
            return;
        }

        // Clear previous output
        setCellOutputs(prev => ({ ...prev, [cellId]: [] }));
        setRunningCells(prev => new Set(prev).add(cellId));

        // Policy Check: Call Backend API first
        const policyResult = await notebookService.executeCodeWithPolicyCheck(notebookId!, code);

        if (!policyResult.success && policyResult.status === 'denied') {
            // Access denied by policy - show error without executing
            setCellOutputs(prev => ({
                ...prev,
                [cellId]: [{
                    type: 'error',
                    ename: 'PermissionError',
                    evalue: policyResult.error || 'Access denied by data policy',
                    traceback: [policyResult.error || 'Access denied by data policy']
                }]
            }));
            setRunningCells(prev => {
                const next = new Set(prev);
                next.delete(cellId);
                return next;
            });
            return;
        }

        // Policy check passed or no policy check needed - execute via WebSocket
        const msgId = notebookService.executeCode(wsRef.current, cellId, code);
        pendingExecutions.current.set(msgId, { cellId, startTime: Date.now() });
    }, [connectionStatus, notebookId]);

    // Cleanup on unmount or notebookId change (no auto-connect)
    useEffect(() => {
    }, [notebookId, disconnect]);

    return {
        connectionStatus,
        cellOutputs,
        runningCells,
        connect,
        disconnect,
        executeCell,
    };
}

// ============ Hook: useNotebookList ============

export function useNotebookList() {
    const [notebooks, setNotebooks] = useState<NotebookDTO[]>([]);
    const [loading, setLoading] = useState(false);
    const [total, setTotal] = useState(0);

    const loadNotebooks = useCallback(async (page = 1, pageSize = 20) => {
        setLoading(true);
        try {
            const response = await notebookService.listNotebooks(page, pageSize, true);
            setNotebooks(response.items);
            setTotal(response.total);
        } finally {
            setLoading(false);
        }
    }, []);

    const createNotebook = useCallback(async (data: {
        name: string;
        language?: NotebookLanguage;
        cluster_config?: ClusterConfig;
    }) => {
        const notebook = await notebookService.createNotebook(data);
        if (notebook) {
            setNotebooks(prev => [notebook, ...prev]);
        }
        return notebook;
    }, []);

    const deleteNotebook = useCallback(async (notebookId: string) => {
        const success = await notebookService.deleteNotebook(notebookId);
        if (success) {
            setNotebooks(prev => prev.filter(n => n.id !== notebookId));
        }
        return success;
    }, []);

    useEffect(() => {
        loadNotebooks();

        // Listen for global updates (e.g. from import or delete in other tabs/components)
        const handleRefresh = () => loadNotebooks();
        window.addEventListener('notebook-list-updated', handleRefresh);

        return () => {
            window.removeEventListener('notebook-list-updated', handleRefresh);
        };
    }, [loadNotebooks]);

    return {
        notebooks,
        loading,
        total,
        loadNotebooks,
        createNotebook,
        deleteNotebook,
    };
}

export default useNotebook;
