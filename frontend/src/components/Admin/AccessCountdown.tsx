import { useEffect, useState } from 'react';

// Remaining time until a trial account expires, ticking every second.
export function formatRemaining(ms: number): string {
  if (ms <= 0) return 'expired';
  const s = Math.floor(ms / 1000);
  const d = Math.floor(s / 86400);
  const hh = String(Math.floor((s % 86400) / 3600)).padStart(2, '0');
  const mm = String(Math.floor((s % 3600) / 60)).padStart(2, '0');
  const ss = String(s % 60).padStart(2, '0');
  return d > 0 ? `${d}d ${hh}:${mm}:${ss}` : `${hh}:${mm}:${ss}`;
}

export function AccessCountdown({ expiresAt, onExpire, className }: {
  expiresAt: string;
  onExpire?: () => void;
  className?: string;
}) {
  const end = new Date(expiresAt).getTime();
  const [now, setNow] = useState(() => Date.now());
  useEffect(() => {
    const t = window.setInterval(() => setNow(Date.now()), 1000);
    return () => window.clearInterval(t);
  }, []);
  const left = end - now;
  useEffect(() => {
    if (left <= 0) onExpire?.();
  }, [left <= 0]); // fire once when crossing zero
  return (
    <span className={className ?? (left <= 0 ? 'text-destructive' : left < 15 * 60 * 1000 ? 'text-amber-600' : '')}>
      {formatRemaining(left)}
    </span>
  );
}
