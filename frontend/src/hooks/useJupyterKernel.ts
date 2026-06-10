/**
 * useJupyterKernel Hook
 * Connects to kernel via Backend API (with WebSocket proxy)
 * Supports multiple independent kernel sessions per notebook
 */
import { useState, useCallback, useEffect, useRef } from 'react';
import axios from 'axios';
import { toast } from 'sonner';
import { executeCodeWithPolicyCheck } from '@/services/notebookService';
import { devLog } from '@/lib/debug';
// Ensure axios interceptors are registered (authService constructor sets up Bearer token)
import '@/services/authService';

export type ConnectionStatus = 'disconnected' | 'connecting' | 'connected' | 'starting' | 'disconnecting' | 'error' | 'dead';
export type NotebookLanguage = 'python' | 'scala' | 'sql';

function isScalaToolingCrash(text: string): boolean {
    const t = text || '';
    if (!t) return false;
    // semanticdb/scalameta mismatch
    if (t.includes('java.lang.NoSuchMethodError') &&
        (t.includes('scala.meta.internal.semanticdb') ||
            t.includes('ScalaInterpreterInspections') ||
            t.includes('semanticdb'))) {
        return true;
    }
    // presentation compiler thread race
    if (t.includes('Race condition detected: You are running a presentation compiler method outside the PC thread') ||
        (t.includes('presentation compiler') && t.includes('PC thread'))) {
        return true;
    }
    return false;
}

export interface CellOutput {
    type: 'stream' | 'result' | 'error';
    text?: string;
    data?: Record<string, unknown>;
    ename?: string;
    evalue?: string;
    traceback?: string[];
}

// Each notebook has its own kernel session
interface KernelSession {
    notebookId: string;
    kernelId: string | null;
    ws: WebSocket | null;
    status: ConnectionStatus;
    deadReason?: string;  // Reason for kernel death (set when status is 'dead')
    // Pod spawn progress for k8s_per_user mode. Shown verbatim in the UI so
    // user sees "Pulling image...", "Container starting...", "Shutting down..."
    // instead of a silent spinner. Empty when no pod transition is in flight.
    podPhase: string;
    podMessage: string;
    outputs: Record<string, CellOutput[]>;
    runningCells: Set<string>;
    pendingCells: Set<string>;  // Track cells queued for execution (Run All)
    executionCounts: Record<string, number>;  // Cell ID → execution count (In [N]:)
    executionTimes: Record<string, number>;   // Cell ID → last execution duration (ms) for this session
    executedCells: Set<string>;  // Track cells that have completed execution
    pendingExecutions: Map<string, { cellId: string; resolve: () => void; hadError?: boolean; startedAt: number }>;
    pendingCompletions: Map<string, { resolve: (result: CompletionResult) => void }>;
    pendingInspections: Map<string, { resolve: (result: InspectionResult) => void }>;
}

export interface CompletionResult {
    matches: string[];
    cursor_start: number;
    cursor_end: number;
    metadata: Record<string, unknown>;
    status: 'ok' | 'error';
}

export interface InspectionResult {
    found: boolean;
    data: Record<string, unknown>;
    metadata: Record<string, unknown>;
    status: 'ok' | 'error';
}

// Global kernel session manager (singleton across all hook instances)
class KernelSessionManager {
    private sessions = new Map<string, KernelSession>();
    private listeners = new Map<string, Set<() => void>>();

    getSession(notebookId: string): KernelSession {
        if (!this.sessions.has(notebookId)) {
            this.sessions.set(notebookId, {
                notebookId,
                kernelId: null,
                ws: null,
                status: 'disconnected',
                podPhase: '',
                podMessage: '',
                outputs: {},
                runningCells: new Set(),
                pendingCells: new Set(),
                executionCounts: {},
                executionTimes: {},
                executedCells: new Set(),
                pendingExecutions: new Map(),
                pendingCompletions: new Map(),
                pendingInspections: new Map(),
            });
        }
        return this.sessions.get(notebookId)!;
    }

    updateSession(notebookId: string, updates: Partial<KernelSession>) {
        const session = this.getSession(notebookId);
        Object.assign(session, updates);
        this.notifyListeners(notebookId);
    }

    subscribe(notebookId: string, callback: () => void) {
        if (!this.listeners.has(notebookId)) {
            this.listeners.set(notebookId, new Set());
        }
        this.listeners.get(notebookId)!.add(callback);
    }

    unsubscribe(notebookId: string, callback: () => void) {
        this.listeners.get(notebookId)?.delete(callback);
    }

    private notifyListeners(notebookId: string) {
        this.listeners.get(notebookId)?.forEach(cb => cb());
    }

    deleteSession(notebookId: string) {
        const session = this.sessions.get(notebookId);
        if (session?.ws) {
            session.ws.close();
        }
        this.sessions.delete(notebookId);
        this.listeners.delete(notebookId);
    }
}

// Singleton session manager
const sessionManager = new KernelSessionManager();

// kernelProxyUrl: optional, e.g. "/api/v1/kernel/{sessionId}/{token}"
// When provided, connects directly to Jupyter Kernel Gateway via proxy
// instead of using the standard /api/v1/notebooks/{id}/kernel/* endpoints
export function useJupyterKernel(notebookId: string, kernelProxyUrl?: string) {
    const [, forceUpdate] = useState({});
    const pageHiddenRef = useRef(typeof document !== 'undefined' ? document.hidden : false);
    const lastHiddenAtRef = useRef(0);

    // Get session for this specific notebook
    const session = sessionManager.getSession(notebookId);

    // Subscribe to session updates
    useEffect(() => {
        const callback = () => forceUpdate({});
        sessionManager.subscribe(notebookId, callback);
        return () => sessionManager.unsubscribe(notebookId, callback);
    }, [notebookId]);

    // After a page reload the in-memory podPhase is empty even if the user's
    // pod is still spawning or terminating in the cluster. Restore the state
    // from the backend so the UI button stays disabled across reloads.
    useEffect(() => {
        if (!notebookId || kernelProxyUrl) return; // proxy mode has no per-user pod
        let cancelled = false;
        (async () => {
            const initial = await axios.get('/api/v1/kernel/spawn-status').catch(() => null);
            if (cancelled || !initial) return;
            let phase: string = initial.data?.phase || '';
            let message: string = initial.data?.message || '';
            if (!phase || phase === 'ready') return; // nothing to restore

            sessionManager.updateSession(notebookId, { podPhase: phase, podMessage: message });

            // Resume polling until the in-flight transition completes — stop on
            // 'ready', 'failed', or (row gone after seeing a non-empty phase).
            let sawNonEmpty = true; // initial phase was non-empty by the guard above
            const deadline = Date.now() + 5 * 60 * 1000;
            while (!cancelled && Date.now() < deadline) {
                await new Promise(r => setTimeout(r, 1500));
                if (cancelled) return;
                const tick = await axios.get('/api/v1/kernel/spawn-status').catch(() => null);
                if (!tick) continue;
                phase = tick.data?.phase || '';
                message = tick.data?.message || '';
                sessionManager.updateSession(notebookId, { podPhase: phase, podMessage: message });
                if (phase) sawNonEmpty = true;
                if (phase === 'failed') return;
                if (phase === 'ready') return;
                if (phase === '' && sawNonEmpty) return;
            }
        })();
        return () => { cancelled = true; };
    }, [notebookId, kernelProxyUrl]);

    // Handle kernel messages
    const handleKernelMessage = useCallback((msg: any) => {
        const session = sessionManager.getSession(notebookId);
        const msgType = msg.header?.msg_type || msg.msg_type;
        const parentMsgId = msg.parent_header?.msg_id;

        // Find which cell this message belongs to
        const execution = session.pendingExecutions.get(parentMsgId);
        const cellId = execution?.cellId;

        // Debug: log ALL messages to see what Almond sends
        // devLog(`[JupyterKernel][${notebookId}] RAW MESSAGE:`, {
        //     msgType,
        //     parentMsgId,
        //     cellId: cellId || 'NOT FOUND',
        //     pendingSize: session.pendingExecutions.size
        // });

        // Handle kernel status messages (these don't have cellId)
        if (msgType === 'status') {
            const executionState = msg.content?.execution_state;
            devLog(`[JupyterKernel][${notebookId}] Kernel status: ${executionState}`);

            // When kernel becomes idle for the first time, it's ready
            if (executionState === 'idle' && session.status === 'starting') {
                devLog(`[JupyterKernel][${notebookId}] ✅ Kernel ready (status=idle), marking as connected`);
                // Clear pod phase too — once kernel is talking to us the spawn
                // progress message is no longer useful and we want the UI to
                // show "Connected" cleanly.
                sessionManager.updateSession(notebookId, { status: 'connected', podPhase: '', podMessage: '' });
            }
            return;
        }

        // Handle completion reply (doesn't have cellId in execution map, but present in pendingCompletions)
        if (msgType === 'complete_reply') {
            const completion = session.pendingCompletions?.get(parentMsgId);
            if (completion) {
                // devLog(`[JupyterKernel][${notebookId}] ✅ Completion received for ${parentMsgId}`);
                completion.resolve(msg.content);
                session.pendingCompletions?.delete(parentMsgId);
            }
            return;
        }

        // Handle inspection reply
        if (msgType === 'inspect_reply') {
            const inspection = session.pendingInspections?.get(parentMsgId);
            if (inspection) {
                inspection.resolve(msg.content);
                session.pendingInspections?.delete(parentMsgId);
            }
            return;
        }

        // Tooling crash path (Scala hover/inspect): semanticdb mismatch / presentation compiler race.
        // These messages often arrive without a cellId mapping.
        if (msgType === 'stream') {
            const streamText = msg.content?.text || '';
            if (isScalaToolingCrash(streamText)) {
                window.dispatchEvent(new CustomEvent('kernel-tooling-crash', { detail: { notebookId, kind: 'scala-tooling' } }));
            }
        }
        if (msgType === 'error') {
            const tbText = Array.isArray(msg.content?.traceback) ? msg.content.traceback.join('\n') : '';
            const errText = `${msg.content?.ename || ''} ${msg.content?.evalue || ''}\n${tbText}`;
            if (isScalaToolingCrash(errText)) {
                window.dispatchEvent(new CustomEvent('kernel-tooling-crash', { detail: { notebookId, kind: 'scala-tooling' } }));
            }
        }

        // Handle kernel_dead message from API Gateway
        if (msgType === 'kernel_dead') {
            const reason = msg.content?.reason || 'Kernel died unexpectedly';
            const kernelId = msg.content?.kernel_id;
            console.error(`[JupyterKernel][${notebookId}] 💀 Kernel dead:`, { reason, kernelId });

            // Update session status to 'dead' with reason
            sessionManager.updateSession(notebookId, {
                status: 'dead',
                deadReason: reason,
                runningCells: new Set() // Clear running cells
            });

            // Show toast notification
            toast.error('Kernel died', {
                description: reason,
                duration: 10000, // Show longer for important message
            });
            return;
        }

        if (!cellId) {
            // Warn for non-status messages without cellId
            // console.warn(`[JupyterKernel][${notebookId}] ⚠️ Message ignored - no cellId mapping for parentMsgId: ${parentMsgId}, msgType: ${msgType}`);
            return;
        }

        switch (msgType) {
            case 'stream': {
                const streamText = msg.content?.text || '';
                // Ignore Scala tooling crashes (hover/inspect) so they don't pollute cell output.
                if (isScalaToolingCrash(streamText)) break;
                // Skip Spark schema noise from write operations
                if (/("type"\s*:\s*"struct"|Parquet message type:|spark_schema|optional binary \w+)/.test(streamText)) break;
                if (streamText) {
                    // Get fresh outputs to avoid race with executeCell clear
                    const freshSession = sessionManager.getSession(notebookId);
                    const existing = freshSession.outputs[cellId] || [];
                    // Skip stderr if it's an exact duplicate of the last message
                    // Deduplicate: skip if last output is identical stream text
                    const last = existing[existing.length - 1];
                    if (last && last.type === 'stream' && last.text === streamText) break;
                    const newOutputs = { ...freshSession.outputs };
                    newOutputs[cellId] = [...existing, { type: 'stream', text: streamText }];
                    sessionManager.updateSession(notebookId, { outputs: newOutputs });
                }
                break;
            }

            case 'execute_result': {
                // Show execute_result for Python
                // For Scala, show if it's a meaningful result (not variable assignment)
                const resultText = msg.content?.data?.['text/plain'];
                if (resultText) {
                    // Skip Scala variable definitions like "x = 10" or "res0: Int = 10"
                    // Skip Spark schema outputs like {"type":"struct","fields":[...]}
                    const isScalaAssignment = /^(res\d+:|[a-zA-Z_]\w*\s*[:=])/.test(resultText.trim());
                    const isSchemaOutput = /("type"\s*:\s*"struct"|Parquet message type:)/.test(resultText);
                    if (!isScalaAssignment && !isSchemaOutput) {
                        const freshSession2 = sessionManager.getSession(notebookId);
                        const existing2 = freshSession2.outputs[cellId] || [];
                        const lastResult = existing2[existing2.length - 1];
                        if (lastResult && lastResult.type === 'result' && JSON.stringify(lastResult.data) === JSON.stringify(msg.content?.data)) break;
                        const newOutputs2 = { ...freshSession2.outputs };
                        newOutputs2[cellId] = [...existing2, { type: 'result', data: msg.content?.data }];
                        sessionManager.updateSession(notebookId, { outputs: newOutputs2 });
                    }
                }
                break;
            }

            case 'display_data':
                // Skip display_data for Scala (usually variable assignments)
                // Only show if has rich content (HTML, images) for Python
                if (msg.content?.data?.['text/html'] || msg.content?.data?.['image/png'] || msg.content?.data?.['image/jpeg']) {
                    const freshSession3 = sessionManager.getSession(notebookId);
                    const newOutputs3 = { ...freshSession3.outputs };
                    newOutputs3[cellId] = [
                        ...(newOutputs3[cellId] || []),
                        { type: 'result', data: msg.content?.data }
                    ];
                    sessionManager.updateSession(notebookId, { outputs: newOutputs3 });
                }
                break;

            case 'error':
                {
                    const tbText = Array.isArray(msg.content?.traceback) ? msg.content.traceback.join('\n') : '';
                    const errText = `${msg.content?.ename || ''} ${msg.content?.evalue || ''}\n${tbText}`;
                    // Ignore Scala tooling crashes (hover/inspect) so they don't mark cells failed.
                    if (isScalaToolingCrash(errText)) break;
                    const freshSession4 = sessionManager.getSession(notebookId);
                    const newOutputs4 = { ...freshSession4.outputs };
                    // We keep stream outputs (especially for Scala, they contain error details)
                    const existing = (newOutputs4[cellId] || []);
                    newOutputs4[cellId] = [
                        ...existing,
                        {
                            type: 'error',
                            ename: msg.content?.ename,
                            evalue: msg.content?.evalue,
                            traceback: msg.content?.traceback
                        }
                    ];
                    sessionManager.updateSession(notebookId, { outputs: newOutputs4 });
                    // Mark execution as having error (for Run All stop-on-error)
                    if (execution) execution.hadError = true;
                }
                break;

            case 'execute_reply': {
                // Execution complete
                const currentSession = sessionManager.getSession(notebookId);
                const newRunningCells = new Set(currentSession.runningCells);
                newRunningCells.delete(cellId);

                // Mark cell as executed (regardless of output)
                const newExecutedCells = new Set(currentSession.executedCells);
                newExecutedCells.add(cellId);

                // Mark error from execute_reply status (backup for hadError flag)
                if (msg.content?.status === 'error' && execution) {
                    execution.hadError = true;
                }

                // Track execution count (In [N]:).
                // We don't show the raw kernel exec_count — it gets bumped by
                // hidden system cells (init-spark-context, spark-connect-*) so the
                // first user cell would show "In [2]" or higher, and re-runs would
                // jump non-monotonically when a system cell fires in between.
                // Instead, maintain our own user-only counter: each user cell run
                // gets max(existing user counts) + 1.
                const newExecCounts = { ...currentSession.executionCounts };
                if (cellId && cellId !== 'init-spark-context' && !cellId.startsWith('spark-connect-')) {
                    const userMax = Object.entries(newExecCounts).reduce((m, [id, c]) =>
                        (id !== 'init-spark-context' && !id.startsWith('spark-connect-') && (c as number) > m)
                            ? (c as number) : m, 0);
                    newExecCounts[cellId] = userMax + 1;
                }

                // Record actual execution duration for this cell. Backed by the
                // startedAt timestamp we stamped into pendingExecutions when
                // execute_request was sent. Used by the cell badge so the user
                // sees "1.23s" right after the cell finishes, without waiting
                // for a DB round-trip via last_execution_time_ms.
                const newExecTimes = { ...currentSession.executionTimes };
                if (cellId && execution?.startedAt) {
                    newExecTimes[cellId] = Date.now() - execution.startedAt;
                }

                // Update state BEFORE calling resolve()
                sessionManager.updateSession(notebookId, {
                    runningCells: newRunningCells,
                    executedCells: newExecutedCells,
                    executionCounts: newExecCounts,
                    executionTimes: newExecTimes,
                });

                // Delay resolve to let React paint pending state on remaining cells
                if (execution?.resolve) {
                    setTimeout(() => execution.resolve(), 50);
                }

                // DON'T delete from pendingExecutions yet!
                // Messages can arrive AFTER execute_reply (especially for Scala/Apache Toree)
                // Clean up after a delay to allow late messages
                setTimeout(() => {
                    session.pendingExecutions.delete(parentMsgId);
                }, 5000); // 5 seconds buffer for late messages
                break;
            }
        }
    }, [notebookId]);

    // Track page visibility to avoid noisy "Kernel disconnected" toasts when the
    // browser backgrounds/suspends the tab and drops the WS with code 1006.
    useEffect(() => {
        pageHiddenRef.current = typeof document !== 'undefined' ? document.hidden : false;
        lastHiddenAtRef.current = 0;

        const onVisibility = () => {
            const hidden = document.hidden;
            pageHiddenRef.current = hidden;
            if (hidden) lastHiddenAtRef.current = Date.now();
        };

        document.addEventListener('visibilitychange', onVisibility);
        window.addEventListener('pagehide', onVisibility);
        window.addEventListener('pageshow', onVisibility);

        return () => {
            document.removeEventListener('visibilitychange', onVisibility);
            window.removeEventListener('pagehide', onVisibility);
            window.removeEventListener('pageshow', onVisibility);
        };
    }, [notebookId]);

    // Setup WebSocket connection
    const setupWebSocket = useCallback((kernelId: string) => {
        const wsProtocol = window.location.protocol === 'https:' ? 'wss:' : 'ws:';
        const wsHost = import.meta.env.VITE_BACKEND_WS_URL || `${wsProtocol}//${window.location.host}`;

        const authToken = localStorage.getItem('sparklabx_token');
        let wsUrl: string;
        if (kernelProxyUrl) {
            // Exam mode: connect via kernel proxy → Jupyter Kernel Gateway
            wsUrl = `${wsHost}${kernelProxyUrl}/api/kernels/${kernelId}/channels?token=${authToken}`;
        } else {
            // Standard mode: connect via backend → local Jupyter Gateway
            wsUrl = `${wsHost}/api/v1/notebooks/${notebookId}/kernel/ws/${kernelId}/channels?token=${authToken}`;
        }

        devLog(`[JupyterKernel][${notebookId}] Connecting WebSocket to kernel:`, kernelId);

        const session = sessionManager.getSession(notebookId);

        // Clear stale execution counts if this is a new kernel session.
        // Check both in-memory (session.kernelId) AND localStorage (survives reload)
        // because DB-restored counts from a previous session would otherwise
        // collide with the new kernel's counter (which restarts at 1).
        const prevKernelKey = `sparklabx_kernel_${notebookId}`;
        const prevKernelId = session.kernelId || localStorage.getItem(prevKernelKey);
        if (prevKernelId && prevKernelId !== kernelId) {
            sessionManager.updateSession(notebookId, { executionCounts: {} });
        }
        localStorage.setItem(prevKernelKey, kernelId);

        // Close existing WebSocket completely before creating new one
        if (session.ws) {
            devLog(`[JupyterKernel][${notebookId}] Closing existing WebSocket`);
            const oldWs = session.ws;
            session.ws = null;
            sessionManager.updateSession(notebookId, { ws: null });

            // Force close with code 1000 (normal closure)
            if (oldWs.readyState === WebSocket.OPEN || oldWs.readyState === WebSocket.CONNECTING) {
                oldWs.close(1000, 'Reconnecting');
            }
        }

        const ws = new WebSocket(wsUrl);

        ws.onopen = () => {
            const sess = sessionManager.getSession(notebookId);
            const wasReconnect = ((sess as any)._retryCount || 0) > 0;
            (sess as any)._retryCount = 0;
            if (wasReconnect) {
                devLog(`[JupyterKernel][${notebookId}] WebSocket reconnected — reloading notebook outputs`);
                // Emit event for NotebookPage to reload saved outputs from DB
                window.dispatchEvent(new CustomEvent('kernel-reconnected', { detail: { notebookId } }));
            }
            // Set status to 'starting' - kernel needs time to initialize (especially Almond/Scala)
            sessionManager.updateSession(notebookId, { ws, status: 'starting', kernelId });

            // Send kernel_info_request on Shell channel to prompt the kernel to send status:idle.
            // Almond ignores kernel_info_request on the Control channel (sent by JKG for health-check),
            // but responds correctly when sent on Shell.
            setTimeout(() => {
                if (ws.readyState === WebSocket.OPEN) {
                    const kernelInfoReq = {
                        header: {
                            msg_id: crypto.randomUUID(),
                            msg_type: 'kernel_info_request',
                            username: 'user',
                            session: crypto.randomUUID(),
                            date: new Date().toISOString(),
                            version: '5.3'
                        },
                        parent_header: {},
                        metadata: {},
                        content: {},
                        buffers: [],
                        channel: 'shell'
                    };
                    ws.send(JSON.stringify(kernelInfoReq));
                    devLog(`[JupyterKernel][${notebookId}] Sent kernel_info_request on shell channel`);
                }
            }, 500); // Small delay to let the WS handshake settle

            // Fallback: if kernel doesn't send idle status within 30s, force connected
            setTimeout(() => {
                const currentSession = sessionManager.getSession(notebookId);
                if (currentSession.status === 'starting') {
                    console.warn(`[JupyterKernel][${notebookId}] ⚠️ Kernel still 'starting' after 30s, forcing 'connected' state`);
                    sessionManager.updateSession(notebookId, { status: 'connected', podPhase: '', podMessage: '' });
                }
            }, 30000); // 30s fallback (Almond responds quickly; Spark kernels fall back here)
        };


        ws.onmessage = (event) => {
            // devLog(`[JupyterKernel][${notebookId}] 📨 WebSocket message received:`, event.data?.substring(0, 200));
            try {
                const msg = JSON.parse(event.data);
                // devLog(`[JupyterKernel][${notebookId}] 📨 Parsed message type:`, msg.header?.msg_type || msg.msg_type);
                handleKernelMessage(msg);
            } catch (e) {
                console.error(`[JupyterKernel][${notebookId}] ❌ Failed to parse message:`, e, event.data?.substring(0, 500));
            }
        };

        ws.onerror = () => {
            // Retry silently (kernel may need 20-30s to start, especially Scala/Spark)
            const session = sessionManager.getSession(notebookId);
            const retryCount = (session as any)._retryCount || 0;
            const maxRetries = 30; // 30 retries × 3s = 90s total

            if (retryCount < maxRetries) {
                (session as any)._retryCount = retryCount + 1;
                devLog(`[JupyterKernel][${notebookId}] WebSocket retry (${retryCount + 1}/${maxRetries})...`);
                // Keep status as 'connecting' — don't flash error to user
                sessionManager.updateSession(notebookId, { status: 'connecting' });
                setTimeout(() => {
                    const s = sessionManager.getSession(notebookId);
                    if (s.status === 'connecting') {
                        setupWebSocket(kernelId);
                    }
                }, 3000);
                return;
            }
            console.error(`[JupyterKernel][${notebookId}] WebSocket failed after ${maxRetries} retries`);
            sessionManager.updateSession(notebookId, { status: 'error' });
        };

        ws.onclose = (event) => {
            const currentSession = sessionManager.getSession(notebookId);
            const retryInProgress = currentSession.status === 'connecting';

            const wasConnected = currentSession.status === 'connected' || currentSession.status === 'starting';
            const hadRunningCells = currentSession.runningCells.size > 0;
            const wasAlreadyDead = currentSession.status === 'dead';
            const pageHidden = pageHiddenRef.current;
            const lastHiddenAt = lastHiddenAtRef.current;
            const recentlyHidden = lastHiddenAt > 0 && (Date.now() - lastHiddenAt) < 15000;

            devLog(`[JupyterKernel][${notebookId}] 🔌 WebSocket closed`, {
                code: event.code,
                reason: event.reason || 'No reason provided',
                wasClean: event.wasClean,
                wasConnected,
                hadRunningCells,
                runningCellIds: Array.from(currentSession.runningCells),
                previousStatus: currentSession.status
            });

            // Map WebSocket close codes to user-friendly messages
            const getCloseReason = (code: number, reason: string): string => {
                if (reason) return reason;
                switch (code) {
                    case 1000: return 'Normal closure';
                    case 1001: return 'Kernel shutdown';
                    case 1006: return 'Connection lost (no close frame)';
                    case 1011: return 'Unexpected server error';
                    case 1012: return 'Server restarting';
                    case 1013: return 'Server overloaded';
                    default: return `Connection closed (code: ${code})`;
                }
            };

            // Always resolve pending tooling requests; otherwise Monaco hover/completion
            // can get stuck "loading" if the WS drops mid-request.
            currentSession.pendingCompletions.forEach(c => c.resolve({ matches: [], cursor_start: 0, cursor_end: 0, metadata: {}, status: 'error' }));
            currentSession.pendingCompletions.clear();
            currentSession.pendingInspections.forEach(c => c.resolve({ found: false, data: {}, metadata: {}, status: 'error' }));
            currentSession.pendingInspections.clear();

            // Don't overwrite 'dead' status with 'disconnected' (keep the detailed reason).
            // Also don't overwrite 'disconnecting' — that's a user-initiated tear-down in
            // progress and disconnect() will land the final 'disconnected' itself after
            // its min-visible delay (otherwise the badge skips the intermediate state).
            const userTearingDown = currentSession.status === 'disconnecting';
            if (!wasAlreadyDead && !userTearingDown) {
                sessionManager.updateSession(notebookId, {
                    ws: null,
                    status: retryInProgress ? 'connecting' : 'disconnected',
                    runningCells: new Set(),
                    pendingCells: new Set(),
                });
                // Auto-reconnect after unexpected disconnect (not normal closure or kernel death)
                if (!retryInProgress && wasConnected && event.code !== 1000) {
                    setTimeout(() => {
                        const s = sessionManager.getSession(notebookId);
                        if (s.status === 'disconnected' && s.kernelId) {
                            devLog(`[JupyterKernel][${notebookId}] Auto-reconnecting WebSocket...`);
                            sessionManager.updateSession(notebookId, { status: 'connecting' });
                            setupWebSocket(s.kernelId);
                        }
                    }, 3000);
                }
            } else {
                sessionManager.updateSession(notebookId, { ws: null });
            }

            // Delay the user-facing toast slightly so transient WS drops that auto-recover
            // don't leave a stale "Kernel disconnected" notification while the badge is already connected.
            if (!retryInProgress && wasConnected && !wasAlreadyDead) {
                const closeReason = getCloseReason(event.code, event.reason);
                const shouldSuppressToast = event.code === 1006 && (pageHidden || recentlyHidden);

                if (event.code !== 1000 && !shouldSuppressToast) {
                    setTimeout(() => {
                        const latestSession = sessionManager.getSession(notebookId);
                        const recovered =
                            latestSession.status === 'connected' ||
                            latestSession.status === 'starting' ||
                            latestSession.status === 'connecting' ||
                            (latestSession.ws != null && latestSession.ws !== ws);

                        if (recovered || latestSession.status === 'dead') {
                            return;
                        }

                        if (hadRunningCells) {
                            console.warn(`[JupyterKernel][${notebookId}] ⚠️ Kernel disconnected while ${currentSession.runningCells.size} cells were running!`);
                            toast.error('Kernel disconnected', {
                                description: `${closeReason}. ${currentSession.runningCells.size} cell(s) were running.`,
                                duration: 8000,
                            });
                        } else {
                            toast.warning('Kernel disconnected', {
                                description: closeReason,
                            });
                        }
                    }, 3500);
                }
            }
        };
    }, [notebookId, kernelProxyUrl, handleKernelMessage]);

    // Check if kernel is active and reconnect if so.
    // Pass expectedKernelName to skip reconnecting to a kernel of the wrong type
    // (e.g. don't connect a scala notebook to a running pyspark kernel).
    const checkConnection = useCallback(async (expectedKernelName?: string) => {
        try {
            if (kernelProxyUrl) {
                // Exam mode: just check if kernels exist, don't auto-connect
                // (let connect() handle language matching)
                const resp = await axios.get(`${kernelProxyUrl}/api/kernels`);
                if (resp.data && resp.data.length > 0) {
                    devLog(`[JupyterKernel][${notebookId}] Found ${resp.data.length} kernels via proxy, ready to connect`);
                }
                sessionManager.updateSession(notebookId, { status: 'disconnected' });
                return;
            }

            const statusParams = expectedKernelName ? `?kernel_name=${expectedKernelName}` : '';
            const response = await axios.get(`/api/v1/notebooks/${notebookId}/kernel/status${statusParams}`);
            if (response.data && response.data.status === 'connected') {
                const runningName: string = response.data.kernel_name || '';
                if (expectedKernelName && runningName && runningName !== expectedKernelName) {
                    devLog(`[JupyterKernel][${notebookId}] Kernel mismatch — running: ${runningName}, expected: ${expectedKernelName}. Staying disconnected.`);
                    sessionManager.updateSession(notebookId, { status: 'disconnected' });
                    return;
                }
                devLog(`[JupyterKernel][${notebookId}] Found active session (${runningName}), connecting...`);
                sessionManager.updateSession(notebookId, { status: 'connecting' });
                setupWebSocket(response.data.kernel_id);
            } else {
                sessionManager.updateSession(notebookId, { status: 'disconnected' });
            }
        } catch (error) {
            console.error(`[JupyterKernel][${notebookId}] Failed to check status:`, error);
            sessionManager.updateSession(notebookId, { status: 'disconnected' });
        }
    }, [notebookId, kernelProxyUrl, setupWebSocket]);


    // Fast in-pod kernel restart — kills the kernel process inside the pod
    // and starts a fresh one, WITHOUT destroying the pod itself. ~1-2s vs
    // ~5-30s for full pod respawn. Use this for "Restart" UX; use disconnect+
    // shutdown only when the user genuinely wants to free the pod.
    const restart = useCallback(async () => {
        const sess = sessionManager.getSession(notebookId);
        if (!sess.kernelId) return;
        try {
            if (kernelProxyUrl) {
                await axios.post(`${kernelProxyUrl}/api/kernels/${sess.kernelId}/restart`);
            } else {
                // Backend ProxyHTTP at /notebooks/:id/kernel/api/* forwards to the
                // user's gateway, hitting Jupyter's restart endpoint on the live pod.
                await axios.post(`/api/v1/notebooks/${notebookId}/kernel/api/kernels/${sess.kernelId}/restart`);
            }
            // In-flight executions are dead; clear so UI doesn't show stuck spinners.
            // Also clear executionCounts — new kernel restarts from 1, stale max
            // counts from prev session would push display negative via systemCellOffset.
            sessionManager.updateSession(notebookId, {
                runningCells: new Set(),
                pendingCells: new Set(),
                executionCounts: {},
                status: 'starting', // kernel will send idle status once restart finishes
            });
            sess.pendingExecutions.forEach(p => p.resolve?.());
            sess.pendingExecutions.clear();
        } catch (e) {
            console.error(`[JupyterKernel][${notebookId}] restart failed`, e);
            throw e;
        }
    }, [notebookId, kernelProxyUrl]);

    // Poll /kernel/spawn-status until the pod reaches a stable state.
    //   until='ready' → stop on 'ready' / 'failed' / (terminating→empty transition)
    //   until='empty' → stop when row gone (phase='') / 'failed'
    // The terminating→empty case lets connect's retry loop kick off a fresh
    // spawn once the previous pod is fully gone instead of polling forever.
    // Side effect: updates session.podPhase / podMessage every tick so UI is live.
    const trackPodStatus = useCallback(async (until: 'ready' | 'empty' = 'ready') => {
        const deadline = Date.now() + 5 * 60 * 1000;
        let sawNonEmpty = false;
        while (Date.now() < deadline) {
            try {
                const resp = await axios.get('/api/v1/kernel/spawn-status');
                const phase: string = resp.data?.phase || '';
                const message: string = resp.data?.message || '';
                sessionManager.updateSession(notebookId, { podPhase: phase, podMessage: message });
                if (phase) sawNonEmpty = true;
                if (phase === 'failed') return;
                if (until === 'ready' && phase === 'ready') return;
                if (until === 'empty' && phase === '') return;
                // 'ready' caller: row was cleared (e.g. terminating finished).
                // Stop so caller can POST /connect for a fresh spawn.
                if (until === 'ready' && phase === '' && sawNonEmpty) return;
            } catch {
                // Transient network blip — keep polling.
            }
            await new Promise(r => setTimeout(r, 1500));
        }
    }, [notebookId]);

    // Connect to kernel via Backend API or Kernel Proxy
    const connect = useCallback(async (language: NotebookLanguage = 'python', kernelName?: string) => {
        try {
            sessionManager.updateSession(notebookId, { status: 'connecting' });

            if (kernelProxyUrl) {
                // Exam mode: connect directly to Jupyter Kernel Gateway via proxy
                devLog(`[JupyterKernel][${notebookId}] Connecting via kernel proxy:`, kernelProxyUrl);

                const resolvedKernelName = kernelName || (
                    language === 'scala' ? 'scala212' :
                        language === 'python' ? 'pyspark' : 'pyspark'
                );

                // Try listing existing kernels first
                try {
                    const listResp = await axios.get(`${kernelProxyUrl}/api/kernels`);
                    if (listResp.data && listResp.data.length > 0) {
                        const match = listResp.data.find((k: any) => k.name === resolvedKernelName);
                        if (match) {
                            devLog(`[JupyterKernel][${notebookId}] Using existing ${resolvedKernelName} kernel:`, match.id);
                            setupWebSocket(match.id);
                            return;
                        }
                        // No matching kernel found — fall through to create new one
                        devLog(`[JupyterKernel][${notebookId}] No ${resolvedKernelName} kernel found, creating new one`);
                    }
                } catch { /* no existing kernels */ }

                // Create new kernel
                try {
                    const createResp = await axios.post(`${kernelProxyUrl}/api/kernels`, { name: resolvedKernelName });
                    if (createResp.data?.id) {
                        devLog(`[JupyterKernel][${notebookId}] Created kernel:`, createResp.data.id);
                        setupWebSocket(createResp.data.id);
                        return;
                    }
                } catch (err: any) {
                    console.error(`[JupyterKernel][${notebookId}] Failed to create kernel:`, err?.response?.data || err);
                }

                sessionManager.updateSession(notebookId, { status: 'error' });
                return;
            }

            // Standard mode: connect via backend notebook API
            devLog(`[JupyterKernel][${notebookId}] Connecting via backend API...`);

            const langLower = language.toLowerCase();
            devLog(`[JupyterKernel][${notebookId}] Requesting kernel:`, { language: langLower, kernel_name: kernelName });

            const payload: { tier: string; language: string; kernel_name?: string } = {
                tier: "medium",
                language: langLower
            };
            if (kernelName) {
                payload.kernel_name = kernelName;
            }

            // Async connect protocol:
            //   200 + kernel_id        → pod ready, kernel session created → setup WS
            //   202 + {phase, message} → pod still spawning → poll spawn-status, retry
            // Retry loop bounded to 5 minutes total so a stuck FailedScheduling
            // can't hang the UI forever.
            const totalDeadline = Date.now() + 5 * 60 * 1000;
            let response;
            while (true) {
                try {
                    response = await axios.post(
                        `/api/v1/notebooks/${notebookId}/kernel/connect`,
                        payload,
                        { timeout: 30_000 },
                    );
                } catch (apiError: any) {
                    console.error(`[JupyterKernel][${notebookId}] Backend API failed:`, apiError?.response?.data || apiError);
                    sessionManager.updateSession(notebookId, { status: 'error' });
                    return;
                }

                if (response?.data?.kernel_id) {
                    break; // pod ready, kernel created
                }

                // 202 — pod still spawning. Update phase, poll until ready, retry.
                if (response?.status === 202 || response?.data?.status === 'spawning') {
                    sessionManager.updateSession(notebookId, {
                        podPhase: response.data?.phase || 'spawning',
                        podMessage: response.data?.message || 'Spawning kernel pod...',
                    });

                    if (Date.now() >= totalDeadline) {
                        console.error(`[JupyterKernel][${notebookId}] Kernel pod did not become ready within 5 minutes`);
                        sessionManager.updateSession(notebookId, { status: 'error' });
                        return;
                    }

                    await trackPodStatus('ready'); // blocks until phase=ready/failed
                    // If spawn ended in 'failed', surface the error instead of looping forever.
                    const cur = sessionManager.getSession(notebookId);
                    if (cur.podPhase === 'failed') {
                        console.error(`[JupyterKernel][${notebookId}] Pod spawn failed: ${cur.podMessage}`);
                        sessionManager.updateSession(notebookId, { status: 'error' });
                        return;
                    }
                    continue; // retry POST /connect — should hit 200 path now
                }

                console.error(`[JupyterKernel][${notebookId}] Invalid response from backend:`, response?.data);
                sessionManager.updateSession(notebookId, { status: 'error' });
                return;
            }

            const { kernel_id, kernel_name: kn, language: kernelLanguage } = response.data;
            devLog(`[JupyterKernel][${notebookId}] Kernel session created/retrieved:`, { kernel_id: kernel_id, kernel_name: kn, language: kernelLanguage });

            setupWebSocket(kernel_id);

        } catch (error) {
            console.error(`[JupyterKernel][${notebookId}] Connection failed:`, error);
            sessionManager.updateSession(notebookId, { status: 'error' });
        }
    }, [notebookId, kernelProxyUrl, setupWebSocket]);

    // Disconnect from kernel
    // Flip session.status to 'disconnecting' without running the
    // disconnect side effects. Useful for Shutdown: we want the
    // badge to show "Disconnecting..." while the backend kills the
    // kernel, and we need it set BEFORE ws.onclose fires (the
    // close handler checks this status to know it should leave
    // status alone instead of overwriting with 'disconnected').
    const markDisconnecting = useCallback(() => {
        sessionManager.updateSession(notebookId, { status: 'disconnecting' });
    }, [notebookId]);

    const disconnect = useCallback(async () => {
        try {
            const session = sessionManager.getSession(notebookId);

            // Flip the badge to "Disconnecting…" before the DELETE
            // round-trip so the user gets feedback (k8s_per_user
            // mode can take a few seconds to ack).
            sessionManager.updateSession(notebookId, { status: 'disconnecting' });

            // Close WebSocket
            if (session.ws) {
                session.ws.close();
            }

            // Tell backend to disconnect. Pair with a min-duration
            // delay so the intermediate state stays visible long
            // enough to register on fast (docker-compose) backends
            // where DELETE often returns in <50ms.
            const minVisible = new Promise(r => setTimeout(r, 800));
            try {
                await Promise.all([
                    axios.delete(`/api/v1/notebooks/${notebookId}/kernel/disconnect`),
                    minVisible,
                ]);
            } catch (e) {
                console.warn(`[JupyterKernel][${notebookId}] Failed to notify backend of disconnect:`, e);
                await minVisible;
            }

            sessionManager.updateSession(notebookId, {
                ws: null,
                kernelId: null,
                status: 'disconnected',
                runningCells: new Set(),
                executedCells: new Set()
            });
            session.pendingExecutions.clear();
            session.pendingCompletions.forEach(c => c.resolve({ matches: [], cursor_start: 0, cursor_end: 0, metadata: {}, status: 'error' }));
            session.pendingCompletions.clear();
            session.pendingInspections.forEach(c => c.resolve({ found: false, data: {}, metadata: {}, status: 'error' }));
            session.pendingInspections.clear();

            devLog(`[JupyterKernel][${notebookId}] Disconnected`);

        } catch (error) {
            console.error(`[JupyterKernel][${notebookId}] Disconnect failed:`, error);
        }
    }, [notebookId]);

    // Execute code in a cell
    // Options: silent = don't broadcast output, store_history = save in execution history
    const executeCell = useCallback(async (
        cellId: string,
        code: string,
        onComplete?: (error?: boolean) => void,
        options?: { silent?: boolean; storeHistory?: boolean }
    ) => {
        const session = sessionManager.getSession(notebookId);
        const ws = session.ws;

        if (!ws || session.status !== 'connected') {
            console.error(`[JupyterKernel][${notebookId}] Not connected`);
            toast.error('Kernel not connected', {
                description: 'Please wait for the kernel to connect or try reconnecting.',
            });
            return;
        }

        // Kernel has a single execution slot — clicking Run on cell B while
        // cell A is still running would (a) queue B on the kernel side and
        // (b) make the frontend mark both as 'running'. Then a Stop on
        // either cell sends one SIGINT that cancels whichever the kernel
        // happens to be executing, looking to the user like "stopping one
        // cell also stopped the other". Refuse the second click instead.
        // Run All has its own queue (executeAllCells chains via onComplete)
        // and is not affected.
        const otherRunning = [...session.runningCells].filter(id => id !== cellId);
        if (otherRunning.length > 0) {
            toast.warning('Kernel is busy', {
                description: 'Another cell is running. Wait for it to finish or click Stop on it first.',
            });
            return;
        }

        // Policy Check: Skip for system-generated cells and exam mode (kernel proxy).
        // 'init-spark-context' is the auto-init cell injected by NotebookPage.
        const isSystemCell = cellId?.startsWith('spark-connect-')
            || cellId === 'init-spark-context'
            || false;
        const skipPolicyCheck = isSystemCell || !!kernelProxyUrl;

        if (!skipPolicyCheck) {
            devLog(`[JupyterKernel][${notebookId}] Checking policy for cell ${cellId}`);
            const policyResult = await executeCodeWithPolicyCheck(notebookId, code);

            if (!policyResult.success && policyResult.status === 'denied') {
                // Access denied by policy - show error without executing
                console.warn(`[JupyterKernel][${notebookId}] Policy denied for cell ${cellId}:`, policyResult.error);

                const errorOutput = [{
                    type: 'error' as const,
                    ename: 'PermissionError',
                    evalue: policyResult.error || 'Access denied by data policy',
                    traceback: [policyResult.error || 'Access denied by data policy']
                }];

                sessionManager.updateSession(notebookId, {
                    outputs: { ...session.outputs, [cellId]: errorOutput },
                    runningCells: new Set() // Not running
                });

                toast.error('Access Denied', {
                    description: policyResult.error || 'You do not have permission to access one or more tables'
                });

                if (onComplete) onComplete();
                return;
            }
        } else {
            devLog(`[JupyterKernel][${notebookId}] Skipping policy check for system cell ${cellId}`);
        }

        devLog(`[JupyterKernel][${notebookId}] Executing cell ${cellId}`, options?.silent ? '(silent)' : '');

        // Clear previous output and remove from pending (if was queued)
        const newOutputs = { ...session.outputs, [cellId]: [] };
        const newRunningCells = new Set(session.runningCells).add(cellId);
        const newPendingCells = new Set(session.pendingCells);
        newPendingCells.delete(cellId);

        sessionManager.updateSession(notebookId, {
            outputs: newOutputs,
            runningCells: newRunningCells,
            pendingCells: newPendingCells
        });

        // Create message ID
        const msgId = crypto.randomUUID();

        // Create execute_request message
        // silent: true = don't broadcast output to other frontends
        // store_history: false = don't save in execution count/history
        const executeRequest = {
            header: {
                msg_id: msgId,
                msg_type: 'execute_request',
                username: 'user',
                session: crypto.randomUUID(),
                date: new Date().toISOString(),
                version: '5.3'
            },
            parent_header: {},
            metadata: {},
            content: {
                code,
                silent: options?.silent ?? false,
                store_history: options?.storeHistory ?? true,
                user_expressions: {},
                allow_stdin: false,
                stop_on_error: true
            },
            buffers: [],
            channel: 'shell'
        };

        // Track this execution. startedAt is captured here (vs in execute_reply
        // metadata) so we measure WS-round-trip-inclusive wall time — what the
        // user actually waited for, not just kernel CPU time.
        session.pendingExecutions.set(msgId, {
            cellId,
            hadError: false,
            startedAt: Date.now(),
            resolve: () => {
                const exec = session.pendingExecutions.get(msgId);
                if (onComplete) onComplete(exec?.hadError);
            }
        });

        // Send message
        devLog(`[JupyterKernel][${notebookId}] 📤 Sending execute_request for cell ${cellId}, msgId: ${msgId}`);
        ws.send(JSON.stringify(executeRequest));
        devLog(`[JupyterKernel][${notebookId}] ✅ Execute request sent, waiting for response...`);

    }, [notebookId, kernelProxyUrl]);

    // Execute all cells sequentially (for Run All)
    const executeAllCells = useCallback((cells: Array<{ id: string; code: string; type: string }>) => {
        const session = sessionManager.getSession(notebookId);

        if (!session.ws || session.status !== 'connected') {
            toast.error('Kernel not connected');
            return;
        }

        // Filter only code cells
        const codeCells = cells.filter(c => c.type === 'code');
        if (codeCells.length === 0) return;

        devLog(`[JupyterKernel][${notebookId}] Run All: ${codeCells.length} cells`);

        // Mark all cells as pending (except first which will run immediately)
        const pendingCellIds = new Set(codeCells.slice(1).map(c => c.id));
        sessionManager.updateSession(notebookId, { pendingCells: pendingCellIds });

        // Execute cells sequentially using onComplete callback chain
        let cellIndex = 0;

        const executeNext = (error?: boolean) => {
            if (error) {
                const remaining = codeCells.length - cellIndex;
                sessionManager.updateSession(notebookId, { pendingCells: new Set() });
                toast.error(remaining > 0
                    ? `Run All stopped: ${remaining} cell(s) skipped due to error`
                    : 'Run All completed with error');
                return;
            }
            if (cellIndex >= codeCells.length) {
                devLog(`[JupyterKernel][${notebookId}] Run All complete`);
                return;
            }

            const cell = codeCells[cellIndex];
            cellIndex++;

            executeCell(cell.id, cell.code, executeNext);
        };

        // Start with first cell
        executeNext();

    }, [notebookId, executeCell]);

    // Clear all pending cells (for canceling Run All)
    const clearPendingCells = useCallback(() => {
        sessionManager.updateSession(notebookId, { pendingCells: new Set() });
    }, [notebookId]);

    // Cleanup on unmount
    useEffect(() => {
        return () => {
            // Don't delete session on unmount - keep it alive for reconnection
            // Only close if explicitly disconnected
        };
    }, [notebookId]);

    // Restore outputs + execution counts from database
    const restoreOutputs = useCallback((cellOutputsFromDB: Record<string, any>, executionCountsFromDB: Record<string, number> = {}) => {
        const restoredOutputs: Record<string, CellOutput[]> = {};

        Object.entries(cellOutputsFromDB).forEach(([cellId, savedOutput]) => {
            if (savedOutput?.outputs && Array.isArray(savedOutput.outputs)) {
                restoredOutputs[cellId] = savedOutput.outputs;
            }
        });

        sessionManager.updateSession(notebookId, {
            outputs: restoredOutputs,
            executionCounts: executionCountsFromDB,
        });
    }, [notebookId]);

    // Get current outputs for a cell (always returns fresh value from session manager)
    const getCellOutputs = useCallback((cellId: string): CellOutput[] => {
        const session = sessionManager.getSession(notebookId);
        return session.outputs[cellId] || [];
    }, [notebookId]);

    // Heartbeat to keep session alive
    // Send ping every 2 minutes to update LastActivity on backend
    // This ensures session stays active even if user isn't executing cells
    // Backend idle timeout is 5 minutes, so 2-minute heartbeat provides safety margin
    useEffect(() => {
        if (session.ws && session.status === 'connected') {
            devLog(`[JupyterKernel][${notebookId}] Starting heartbeat (every 2 minutes)`);

            const heartbeatInterval = setInterval(() => {
                if (session.ws && session.ws.readyState === WebSocket.OPEN) {
                    // Send a kernel_info_request to update activity
                    // This is a lightweight message that won't affect execution
                    const msg = {
                        header: {
                            msg_id: crypto.randomUUID(),
                            msg_type: 'kernel_info_request',
                            username: 'user',
                            session: crypto.randomUUID(),
                            date: new Date().toISOString(),
                            version: '5.3'
                        },
                        parent_header: {},
                        metadata: {},
                        content: {}
                    };
                    session.ws.send(JSON.stringify(msg));
                    devLog(`[JupyterKernel][${notebookId}] 💓 Heartbeat sent`);
                }
            }, 2 * 60 * 1000); // 2 minutes

            return () => {
                devLog(`[JupyterKernel][${notebookId}] Stopping heartbeat`);
                clearInterval(heartbeatInterval);
            };
        }
    }, [notebookId, session.ws, session.status]);

    // Clear output for a specific cell
    const clearCellOutput = useCallback((cellId: string) => {
        const session = sessionManager.getSession(notebookId);
        const newOutputs = { ...session.outputs };
        newOutputs[cellId] = [];

        const newExecutedCells = new Set(session.executedCells);
        newExecutedCells.delete(cellId);

        const newExecutionCounts = { ...session.executionCounts };
        delete newExecutionCounts[cellId];

        sessionManager.updateSession(notebookId, {
            outputs: newOutputs,
            executedCells: newExecutedCells,
            executionCounts: newExecutionCounts,
        });
    }, [notebookId]);

    // Wait for kernel to be ready
    const waitForReady = useCallback(async (timeoutMs: number = 90000): Promise<boolean> => {
        const session = sessionManager.getSession(notebookId);

        // If already connected, return true immediately
        if (session.status === 'connected') {
            return true;
        }

        return new Promise((resolve) => {
            // Check function
            const check = () => {
                const currentSession = sessionManager.getSession(notebookId);
                if (currentSession.status === 'connected') {
                    cleanup();
                    resolve(true);
                } else if (currentSession.status === 'error') {
                    cleanup();
                    resolve(false);
                }
            };

            // Cleanup function — references `timeoutId` via closure. Only invoked
            // from async callbacks (timer callback below or subscribe callback),
            // by which point `timeoutId` is already assigned.
            const cleanup = () => {
                clearTimeout(timeoutId);
                sessionManager.unsubscribe(notebookId, check);
            };

            const timeoutId: NodeJS.Timeout = setTimeout(() => {
                cleanup();
                console.warn(`[JupyterKernel][${notebookId}] waitForReady timed out after ${timeoutMs}ms`);
                resolve(false);
            }, timeoutMs);

            // Subscribe to updates
            sessionManager.subscribe(notebookId, check);
        });
    }, [notebookId]);

    // Request code completion
    const requestCompletion = useCallback(async (code: string, cursorPos: number): Promise<CompletionResult | null> => {
        const session = sessionManager.getSession(notebookId);
        const ws = session.ws;

        if (!ws || session.status !== 'connected') {
            return null;
        }

        const msgId = crypto.randomUUID();
        const content = {
            code,
            cursor_pos: cursorPos
        };

        const message = {
            header: {
                msg_id: msgId,
                msg_type: 'complete_request',
                username: 'user',
                session: crypto.randomUUID(),
                date: new Date().toISOString(),
                version: '5.3'
            },
            parent_header: {},
            metadata: {},
            content,
            channel: 'shell'
        };

        return new Promise<CompletionResult>((resolve) => {
            // Register pending completion
            session.pendingCompletions.set(msgId, { resolve });

            // Send request
            ws.send(JSON.stringify(message));

            // Timeout after 5 seconds to prevent hanging
            setTimeout(() => {
                const currentSession = sessionManager.getSession(notebookId);
                if (currentSession.pendingCompletions.has(msgId)) {
                    currentSession.pendingCompletions.delete(msgId);
                    resolve({ matches: [], cursor_start: cursorPos, cursor_end: cursorPos, metadata: {}, status: 'error' });
                }
            }, 5000);
        });
    }, [notebookId]);

    // Request code inspection (hover/docstring)
    const requestInspection = useCallback(async (code: string, cursorPos: number, detailLevel = 0): Promise<InspectionResult | null> => {
        const session = sessionManager.getSession(notebookId);
        const ws = session.ws;

        if (!ws || session.status !== 'connected') {
            return null;
        }

        const msgId = crypto.randomUUID();
        const content = {
            code,
            cursor_pos: cursorPos,
            detail_level: detailLevel
        };

        const message = {
            header: {
                msg_id: msgId,
                msg_type: 'inspect_request',
                username: 'user',
                session: crypto.randomUUID(),
                date: new Date().toISOString(),
                version: '5.3'
            },
            parent_header: {},
            metadata: {},
            content,
            channel: 'shell'
        };

        return new Promise<InspectionResult>((resolve) => {
            // Register pending inspection
            devLog(`[JupyterKernel][${notebookId}] 📤 Sending inspect_request:`, msgId);
            session.pendingInspections.set(msgId, { resolve });

            // Send request
            ws.send(JSON.stringify(message));

            // Timeout after 2.5 seconds (hover should feel snappy)
            setTimeout(() => {
                const currentSession = sessionManager.getSession(notebookId);
                if (currentSession.pendingInspections.has(msgId)) {
                    currentSession.pendingInspections.delete(msgId);
                    resolve({ found: false, data: {}, metadata: {}, status: 'error' });
                }
            }, 2500);
        });
    }, [notebookId]);

    return {
        connectionStatus: session.status,
        deadReason: session.deadReason,  // Reason for kernel death (when status is 'dead')
        podPhase: session.podPhase,      // K8s pod spawn/terminate progress
        podMessage: session.podMessage,  // human-readable phase message
        cellOutputs: session.outputs,
        runningCells: session.runningCells,
        pendingCells: session.pendingCells,
        executionCounts: session.executionCounts,
        executionTimes: session.executionTimes,
        executedCells: session.executedCells,
        connect,
        checkConnection,
        waitForReady,
        disconnect,
        markDisconnecting,
        restart,
        trackPodStatus,
        executeCell,
        executeAllCells,
        clearPendingCells,
        restoreOutputs,
        getCellOutputs,
        clearCellOutput,
        requestCompletion,
        requestInspection, // Exported new function
    };
}
