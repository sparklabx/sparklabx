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

    // Always one decimal on the "used" figure so its width is constant — "0.0/2"
    // and "1.4/2" take the same space, so the widget doesn't jump as values
    // change. The limit is fixed, so trim its trailing ".0".
    const cpuDetail = limitCores > 0 ? `${usedCores.toFixed(1)}/${trim(limitCores)} vCPU` : `${usedCores.toFixed(1)} vCPU`;
    const memDetail = limitGB > 0 ? `${usedGB.toFixed(1)}/${trim(limitGB)} GB` : `${usedGB.toFixed(1)} GB`;
    const title = `Kernel CPU ${cpuPct}% (${cpuDetail}) · RAM ${memPct}% (${memDetail})`;

    // Two flex rows with fixed-width cells so the columns line up. Avoid
    // Tailwind arbitrary grid-cols-[...] which didn't render here (the spans
    // stacked vertically instead of forming columns).
    const Row: React.FC<{ label: string; pct: number; detail: string }> = ({ label, pct, detail }) => (
        <div className="flex items-center gap-1 whitespace-nowrap">
            <span className="text-muted-foreground">{label}</span>
            {/* Just wide enough for "100%"; right-aligned so the detail column
                still lines up between the two rows without a big gap. */}
            <span className={`w-[1.9rem] text-right ${sevColor(pct)}`}>{pct}%</span>
            {!compact && <span className="text-muted-foreground">{detail}</span>}
        </div>
    );

    return (
        <div className="flex flex-col leading-[1.25] text-[10px] tabular-nums shrink-0" title={title}>
            <Row label="CPU" pct={cpuPct} detail={cpuDetail} />
            <Row label="RAM" pct={memPct} detail={memDetail} />
        </div>
    );
};
