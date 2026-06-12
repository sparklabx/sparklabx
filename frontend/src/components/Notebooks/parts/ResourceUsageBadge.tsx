import React, { useEffect, useRef, useState } from 'react';
import axios from 'axios';

interface Usage {
    available: boolean;
    cpu_percent?: number;
    cpu_used_cores?: number;
    cpu_limit_cores?: number;
    mem_used_bytes?: number;
    mem_limit_bytes?: number;
}

// Color a metric by how close it is to its limit.
function sevColor(pct: number): string {
    if (pct >= 80) return 'text-rose-500';
    if (pct >= 60) return 'text-amber-500';
    return 'text-foreground';
}

// Drop a trailing ".0" so whole numbers read cleanly ("2" not "2.0").
function trim(n: number): string {
    return n.toFixed(1).replace(/\.0$/, '');
}

// Live CPU/RAM of the current user's kernel container, as a compact two-row
// readout that lines up in columns:
//
//   CPU  12%   0.2/2 vCPU
//   RAM  34%   0.8/4 GB
//
// Polls /kernel/usage every 4s only while `enabled`. Renders nothing when
// usage isn't available (shared mode, no container, k8s without metrics).
export const ResourceUsageBadge: React.FC<{ enabled: boolean; compact?: boolean }> = ({ enabled, compact = false }) => {
    const [usage, setUsage] = useState<Usage | null>(null);
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
                // keep last good value
            }
            if (aliveRef.current) timer = setTimeout(poll, 4000);
        };
        poll();
        return () => { aliveRef.current = false; clearTimeout(timer); };
    }, [enabled]);

    if (!enabled || !usage?.available) return null;

    const cpuPct = Math.max(0, Math.round(usage.cpu_percent ?? 0));
    const usedCores = usage.cpu_used_cores ?? 0;
    const limitCores = usage.cpu_limit_cores ?? 0;

    const usedGB = (usage.mem_used_bytes ?? 0) / 1024 ** 3;
    const limitGB = (usage.mem_limit_bytes ?? 0) / 1024 ** 3;
    const memPct = limitGB > 0 ? Math.round((usedGB / limitGB) * 100) : 0;

    const cpuDetail = limitCores > 0 ? `${trim(usedCores)}/${trim(limitCores)} vCPU` : `${trim(usedCores)} vCPU`;
    const memDetail = limitGB > 0 ? `${usedGB.toFixed(1)}/${trim(limitGB)} GB` : `${usedGB.toFixed(1)} GB`;
    const title = `Kernel CPU ${cpuPct}% (${cpuDetail}) · RAM ${memPct}% (${memDetail})`;

    return (
        <div
            className="grid grid-cols-[auto_auto_auto] gap-x-1.5 gap-y-0 items-center text-[10px] leading-[1.2] tabular-nums"
            title={title}
        >
            <span className="text-muted-foreground">CPU</span>
            <span className={`text-right ${sevColor(cpuPct)}`}>{cpuPct}%</span>
            {compact ? <span /> : <span className="text-muted-foreground">{cpuDetail}</span>}

            <span className="text-muted-foreground">RAM</span>
            <span className={`text-right ${sevColor(memPct)}`}>{memPct}%</span>
            {compact ? <span /> : <span className="text-muted-foreground">{memDetail}</span>}
        </div>
    );
};
