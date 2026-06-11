import React from 'react';
import { Circle } from 'lucide-react';
import { ConnectionStatus } from '@/hooks/useJupyterKernel';

export const ConnectionStatusBadge: React.FC<{
    status: ConnectionStatus;
    compact?: boolean;
    deadReason?: string;
    sparkInitializing?: boolean;
}> = ({ status, compact = false, deadReason, sparkInitializing }) => {
    const statusConfig: Record<ConnectionStatus, { color: string; label: string; icon?: 'circle' | 'skull' }> = {
        disconnected: { color: 'text-slate-400', label: 'Disconnected' },
        connecting: { color: 'text-blue-500 animate-pulse', label: 'Connecting...' },
        starting: { color: 'text-amber-500 animate-pulse', label: 'Starting...' },
        connected: { color: 'text-emerald-500', label: 'Connected' },
        disconnecting: { color: 'text-amber-500 animate-pulse', label: 'Disconnecting...' },
        error: { color: 'text-rose-500', label: 'Error' },
        dead: { color: 'text-rose-600', label: 'Kernel Dead', icon: 'skull' },
    };

    // When kernel is connected but still booting Spark, show a distinct state
    // so users know why cells are disabled.
    const config = status === 'connected' && sparkInitializing
        ? { color: 'text-amber-500 animate-pulse', label: 'Booting Spark...', icon: 'circle' as const }
        : statusConfig[status];
    const tooltipText = status === 'dead' && deadReason ? `${config.label}: ${deadReason}` : config.label;

    return (
        <div className="flex items-center gap-1" title={tooltipText}>
            {config.icon === 'skull' ? (
                <svg className={`h-3 w-3 ${config.color}`} fill="currentColor" viewBox="0 0 20 20">
                    <path fillRule="evenodd" d="M10 18a8 8 0 100-16 8 8 0 000 16zM8.707 7.293a1 1 0 00-1.414 1.414L8.586 10l-1.293 1.293a1 1 0 101.414 1.414L10 11.414l1.293 1.293a1 1 0 001.414-1.414L11.414 10l1.293-1.293a1 1 0 00-1.414-1.414L10 8.586 8.707 7.293z" clipRule="evenodd" />
                </svg>
            ) : (
                <Circle className={`h-3 w-3 ${config.color} fill-current`} />
            )}
            {!compact && (
                // Fixed width sized to the longest label ("Disconnecting…" /
                // "Booting Spark…") so toolbar items to the right don't
                // shift when the status changes.
                <span className="text-sm text-muted-foreground whitespace-nowrap inline-block w-32">
                    {config.label}
                </span>
            )}
        </div>
    );
};
