import React, { useEffect, useRef, useState } from 'react';
import axios from 'axios';
import { Cpu, MemoryStick } from 'lucide-react';

interface Usage {
    available: boolean;
    cpu_percent?: number;
    mem_used_bytes?: number;
    mem_limit_bytes?: number;
}

// Live CPU/RAM of the current user's kernel container. Polls /kernel/usage
// only while `enabled` (kernel connected). Renders nothing when usage isn't
// available — shared mode, no container yet, or k8s without metrics-server all
// return {available:false}, so the widget simply disappears instead of showing
// zeros or an error.
export const ResourceUsageBadge: React.FC<{ enabled: boolean; compact?: boolean }> = ({ enabled, compact = false }) => {
    const [usage, setUsage] = useState<Usage | null>(null);
    // Avoid a state update (and re-render) after unmount / disable.
    const aliveRef = useRef(true);

    useEffect(() => {
        aliveRef.current = true;
        if (!enabled) {
            setUsage(null);
            return () => { aliveRef.current = false; };
        }
        let timer: ReturnType<typeof setTimeout>;
        const poll = async () => {
            try {
                const res = await axios.get<Usage>('/api/v1/kernel/usage', { timeout: 3000 });
                if (aliveRef.current) setUsage(res.data);
            } catch {
                // Transient failure — keep the last good value, try again next tick.
            }
            if (aliveRef.current) timer = setTimeout(poll, 4000);
        };
        poll();
        return () => { aliveRef.current = false; clearTimeout(timer); };
    }, [enabled]);

    if (!enabled || !usage?.available) return null;

    const cpu = Math.max(0, Math.round(usage.cpu_percent ?? 0));
    const usedGB = (usage.mem_used_bytes ?? 0) / 1024 ** 3;
    const limitGB = (usage.mem_limit_bytes ?? 0) / 1024 ** 3;
    const memPct = limitGB > 0 ? (usedGB / limitGB) * 100 : 0;

    // Color by the hotter of the two signals.
    const severity = Math.max(cpu, memPct);
    const color =
        severity >= 80 ? 'text-rose-500'
            : severity >= 60 ? 'text-amber-500'
                : 'text-muted-foreground';

    const ramText = limitGB > 0
        ? `${usedGB.toFixed(1)}/${limitGB.toFixed(0)} GB`
        : `${usedGB.toFixed(1)} GB`;
    const title = `Kernel CPU ${cpu}% · RAM ${ramText}`;

    return (
        <div className={`flex items-center gap-2 text-xs shrink-0 whitespace-nowrap ${color}`} title={title}>
            <span className="flex items-center gap-1 tabular-nums">
                <Cpu className="size-3" />
                {cpu}%
            </span>
            {!compact && (
                <span className="flex items-center gap-1 tabular-nums">
                    <MemoryStick className="size-3" />
                    {ramText}
                </span>
            )}
        </div>
    );
};
