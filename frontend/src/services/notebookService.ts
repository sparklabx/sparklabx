/**
 * Notebook Service
 * Handles notebook CRUD and WebSocket execution via Backend API
 */
import api from '../config/axios';

// ============ Types ============

export type NotebookLanguage = 'python' | 'scala' | 'sql';
export type CellType = 'code' | 'markdown';
export type ExecutionStatus = 'queued' | 'running' | 'success' | 'error';
export type KernelStatus = 'starting' | 'idle' | 'busy' | 'dead';

export interface ClusterConfig {
    'spark.remote'?: string;
    'spark.executor.memory'?: string;
    'spark.executor.cores'?: number;
    'spark.driver.memory'?: string;
    'spark.jars.packages'?: string;
    [key: string]: string | number | undefined;
}

export interface NotebookCellDTO {
    id: string;
    notebook_id: string;
    type: CellType;
    source: string;
    order: number;
    last_output?: Record<string, unknown>;
    last_execution_time_ms?: number;
    execution_count?: number;
    created_at: string;
    updated_at: string;
}

export interface NotebookDTO {
    id: string;
    name: string;
    description?: string;
    user_id: number;
    language: NotebookLanguage;
    cluster_config: ClusterConfig;
    is_public: boolean;
    tags: string[];
    created_at: string;
    updated_at: string;
}

export interface NotebookDetailDTO extends NotebookDTO {
    cells: NotebookCellDTO[];
}

export interface NotebookListResponse {
    items: NotebookDTO[];
    total: number;
    page: number;
    page_size: number;
}

export interface KernelSpec {
    name: string;
    display_name: string;
    language: string;
    spec?: {
        display_name: string;
        language: string;
        argv?: string[];
    };
}

export interface KernelSpecsResponse {
    default: string;
    kernelspecs: Record<string, KernelSpec>;
}

export interface ExecutionHistoryDTO {
    id: string;
    cell_id?: string;
    notebook_id: string;
    user_id: number;
    source: string;
    result?: Record<string, unknown>;
    status: ExecutionStatus;
    error_message?: string;
    duration_ms?: number;
    created_at: string;
    completed_at?: string;
}

export interface KernelSessionDTO {
    id: string;
    notebook_id: string;
    user_id: number;
    kernel_id: string;
    kernel_name: string;
    status: KernelStatus;
    last_activity: string;
    created_at: string;
    closed_at?: string;
}

// ============ API Functions ============

const BASE_URL = '/api/v1/notebooks';

/**
 * List all notebooks accessible by current user
 */
export async function listNotebooks(
    page = 1,
    pageSize = 20,
    includePublic = true,
): Promise<NotebookListResponse> {
    try {
        const response = await api.get(BASE_URL, {
            params: { page, page_size: pageSize, include_public: includePublic },
        });
        return response.data;
    } catch (error) {
        console.error('Failed to list notebooks:', error);
        return { items: [], total: 0, page, page_size: pageSize };
    }
}

/**
 * Get a notebook with all cells
 */
export async function getNotebook(notebookId: string): Promise<NotebookDetailDTO | null> {
    try {
        const response = await api.get(`${BASE_URL}/${notebookId}`, {
            params: { _t: Date.now() }, // cache bust
        });
        return response.data;
    } catch (error) {
        console.error('Failed to get notebook:', error);
        return null;
    }
}

/**
 * Create a new notebook
 */
export async function createNotebook(data: {
    name: string;
    description?: string;
    language?: NotebookLanguage;
    cluster_config?: ClusterConfig;
    is_public?: boolean;
    tags?: string[];
}): Promise<NotebookDTO | null> {
    try {
        const response = await api.post(BASE_URL, data);
        return response.data;
    } catch (error) {
        console.error('Failed to create notebook:', error);
        return null;
    }
}

/**
 * Update a notebook
 */
export async function updateNotebook(
    notebookId: string,
    updates: Partial<{
        name: string;
        description: string;
        language: NotebookLanguage;
        cluster_config: ClusterConfig;
        is_public: boolean;
        tags: string[];
    }>
): Promise<NotebookDTO | null> {
    try {
        // Convert language to uppercase for database enum if provided
        const payload = { ...updates };
        if (payload.language) {
            payload.language = payload.language.toUpperCase() as NotebookLanguage;
        }
        const response = await api.put(`${BASE_URL}/${notebookId}`, payload);
        return response.data;
    } catch (error) {
        console.error('Failed to update notebook:', error);
        return null;
    }
}

/**
 * Delete a notebook
 */
export async function deleteNotebook(notebookId: string): Promise<boolean> {
    try {
        await api.delete(`${BASE_URL}/${notebookId}`);
        return true;
    } catch (error) {
        console.error('Failed to delete notebook:', error);
        return false;
    }
}

/**
 * Export notebook as HTML file (triggers download)
 */
export async function exportNotebookAsHTML(notebookId: string, name: string): Promise<void> {
    try {
        const response = await api.get(`${BASE_URL}/${notebookId}/export/html`, {
            responseType: 'blob'
        });

        // Create download link
        const blob = new Blob([response.data], { type: 'text/html' });
        const url = window.URL.createObjectURL(blob);
        const a = document.createElement('a');
        a.href = url;
        a.download = `${name}.html`;
        document.body.appendChild(a);
        a.click();
        document.body.removeChild(a);
        window.URL.revokeObjectURL(url);
    } catch (error) {
        console.error('Failed to export notebook:', error);
        throw error;
    }
}

/**
 * Import notebook from JSON or HTML file
 */
export async function importNotebook(file: File): Promise<{ id: string; name: string } | null> {
    try {
        const formData = new FormData();
        formData.append('file', file);

        const response = await api.post(`${BASE_URL}/import`, formData, {
            headers: {
                'Content-Type': 'multipart/form-data'
            }
        });
        return response.data;
    } catch (error) {
        console.error('Failed to import notebook:', error);
        return null;
    }
}

// ============ Cell Operations ============

/**
 * Add a cell to notebook
 */
export async function addCell(
    notebookId: string,
    data: { type: CellType; source: string },
    afterOrder?: number
): Promise<NotebookCellDTO | null> {
    try {
        const payload: any = {
            type: data.type.toLowerCase(),
            source: data.source
        };
        if (afterOrder !== undefined) {
            payload.after_order = afterOrder;
        }
        const response = await api.post(`${BASE_URL}/${notebookId}/cells`, payload);
        return response.data;
    } catch (error) {
        console.error('Failed to add cell:', error);
        return null;
    }
}

/**
 * Update a cell
 */
export async function updateCell(
    notebookId: string,
    cellId: string,
    updates: Partial<{ type: CellType; source: string; order: number; last_output: any; last_execution_time_ms: number; execution_count: number }>
): Promise<NotebookCellDTO | null> {
    try {
        // Convert type to uppercase for database enum if provided
        const payload = { ...updates };
        if (payload.type) {
            payload.type = payload.type.toUpperCase() as CellType;
        }
        const response = await api.put(`${BASE_URL}/${notebookId}/cells/${cellId}`, payload);
        return response.data;
    } catch (error) {
        console.error('Failed to update cell:', error);
        return null;
    }
}

/**
 * Delete a cell
 */
export async function deleteCell(notebookId: string, cellId: string): Promise<boolean> {
    try {
        await api.delete(`${BASE_URL}/${notebookId}/cells/${cellId}`);
        return true;
    } catch (error) {
        console.error('Failed to delete cell:', error);
        return false;
    }
}

/**
 * Reorder cells in a notebook
 */
export async function reorderCells(notebookId: string, cellIds: string[]): Promise<boolean> {
    try {
        await api.post(`${BASE_URL}/${notebookId}/cells/reorder`, { cell_ids: cellIds });
        return true;
    } catch (error) {
        console.error('Failed to reorder cells:', error);
        return false;
    }
}

// ============ Execution History ============

/**
 * Get execution history for a notebook
 */
export async function getExecutionHistory(
    notebookId: string,
    limit = 50
): Promise<ExecutionHistoryDTO[]> {
    try {
        const response = await api.get(`${BASE_URL}/${notebookId}/history`, {
            params: { limit }
        });
        return response.data || [];
    } catch (error) {
        console.error('Failed to get execution history:', error);
        return [];
    }
}

// ============ Kernel Session ============

/**
 * Get active kernel session
 */
export async function getKernelSession(notebookId: string): Promise<KernelSessionDTO | null> {
    try {
        const response = await api.get(`${BASE_URL}/${notebookId}/kernel`);
        return response.data;
    } catch (error) {
        console.error('Failed to get kernel session:', error);
        return null;
    }
}

// ============ WebSocket Connection ============

export interface NotebookWebSocketOptions {
    onMessage: (msg: WebSocketMessage) => void;
    onOpen?: () => void;
    onClose?: () => void;
    onError?: (error: Event) => void;
}

export interface WebSocketMessage {
    header: {
        msg_id: string;
        msg_type: string;
        session: string;
    };
    parent_header?: {
        msg_id?: string;
    };
    content: Record<string, unknown>;
    metadata?: Record<string, unknown>;
}

/**
 * Connect to notebook WebSocket for code execution
 */
export function connectNotebookWebSocket(
    notebookId: string,
    token: string,
    options: NotebookWebSocketOptions
): WebSocket | null {
    try {
        // Get WebSocket URL from environment or construct from API base
        const wsBaseUrl = import.meta.env.VITE_WS_URL ||
            window.location.origin.replace(/^http/, 'ws');

        const wsUrl = `${wsBaseUrl}/api/v1/notebooks/${notebookId}/connect?token=${token}`;

        console.log('[NotebookWS] Connecting to notebook:', notebookId);

        const ws = new WebSocket(wsUrl);

        ws.onopen = () => {
            console.log('[NotebookWS] Connected');
            options.onOpen?.();
        };

        ws.onmessage = (event) => {
            try {
                const msg = JSON.parse(event.data) as WebSocketMessage;
                options.onMessage(msg);
            } catch (e) {
                console.error('[NotebookWS] Failed to parse message:', e);
            }
        };

        ws.onclose = () => {
            console.log('[NotebookWS] Disconnected');
            options.onClose?.();
        };

        ws.onerror = (error) => {
            console.error('[NotebookWS] Error:', error);
            options.onError?.(error);
        };

        return ws;
    } catch (error) {
        console.error('[NotebookWS] Failed to connect:', error);
        return null;
    }
}

/**
 * Execute code via WebSocket
 */
export function executeCode(
    ws: WebSocket,
    cellId: string,
    code: string
): string {
    const msgId = crypto.randomUUID();

    const message = {
        header: {
            msg_id: msgId,
            msg_type: 'execute_request',
            session: crypto.randomUUID(),
            username: '',
            version: '5.3'
        },
        parent_header: {},
        metadata: {
            cell_id: cellId  // Backend uses this for history logging
        },
        content: {
            code,
            silent: false,
            store_history: true,
            user_expressions: {},
            allow_stdin: false,
            stop_on_error: true
        }
    };

    ws.send(JSON.stringify(message));

    return msgId;
}

/**
 * Execute code with policy check.
 * No external policy service in this deployment — always allow.
 */
export async function executeCodeWithPolicyCheck(
    _notebookId: string,
    _code: string
): Promise<{ success: boolean; error?: string; output?: unknown; status?: string }> {
    return { success: true };
}

// ============ Kernel Specs ============

/**
 * Fetch available kernel specifications (Python, Toree, Almond, etc.)
 */
export async function fetchKernelSpecs(): Promise<KernelSpecsResponse> {
    const response = await api.get<KernelSpecsResponse>('/api/v1/notebooks/kernel/specs');
    return response.data;
}

// ============ Export ============

export const notebookService = {
    // Notebook CRUD
    listNotebooks,
    getNotebook,
    createNotebook,
    updateNotebook,
    deleteNotebook,

    // Cell operations
    addCell,
    updateCell,
    deleteCell,
    reorderCells,

    // History
    getExecutionHistory,

    // Kernel
    getKernelSession,
    fetchKernelSpecs,

    // WebSocket
    connectNotebookWebSocket,
    executeCode,
    executeCodeWithPolicyCheck,
};

export default notebookService;
