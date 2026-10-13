import React, { useState } from 'react';
import { useQuery } from '@tanstack/react-query';
import axios from 'axios';
import { Activity, Cpu, MemoryStick, Users as UsersIcon, Server, AlertTriangle } from 'lucide-react';
import { Card, CardContent } from '../ui/card';
import { AccessCountdown } from './AccessCountdown';

interface Point { ts: string; online: number; kernels: number; cpu_cores: number; mem_bytes: number }
interface UsageUser {
  id: string; username: string; email: string; role: string; status: string;
  access_expires_at: string | null; created_at: string; last_seen_at: string | null;
  online: boolean; kernel_running: boolean; cpu_now: number; mem_now: number;
  today_active_minutes: number; active_days_7d: number; active_minutes_7d: number;
  kernel_minutes_7d: number; cpu_core_hours_7d: number; cells_7d: number; cells_total: number;
  active_days_total: number; flags: string[];
}
interface UsageData {
  now: { online: number; kernels: number; cpu_cores: number; mem_bytes: number };
  series: Point[];
  engagement: { approved: number; activated: number; returning: number; pending: number; avg_active_mins: number };
  users: UsageUser[];
}

const gb = (b: number) => (b / 1024 ** 3).toFixed(1);
const pct = (a: number, b: number) => (b ? `${Math.round((a / b) * 100)}%` : '—');
const ago = (iso: string | null) => {
  if (!iso) return 'never';
  const s = (Date.now() - new Date(iso).getTime()) / 1000;
  if (s < 60) return 'just now';
  if (s < 3600) return `${Math.floor(s / 60)}m ago`;
  if (s < 86400) return `${Math.floor(s / 3600)}h ago`;
  return `${Math.floor(s / 86400)}d ago`;
};
const FLAG_INFO: Record<string, string> = {
  heavy_cpu: 'Heavy CPU: ≥10 core-hours in 7 days',
  high_avg_cpu: 'High average CPU while the kernel runs (≥1.5 cores) — check for mining-like use',
  idle_kernel: 'Kernel kept running long while the user was inactive',
};

// Minimal SVG line chart (no chart dependency): one or more series over time.
const LineChart: React.FC<{ points: Point[]; series: { key: keyof Point; label: string; color: string; fmt?: (v: number) => string }[] }> = ({ points, series }) => {
  const W = 600, H = 140, P = 24;
  if (points.length < 2) return <div className="h-[140px] flex items-center justify-center text-xs text-muted-foreground">Collecting data (one point per minute)…</div>;
  const t0 = new Date(points[0].ts).getTime(), t1 = new Date(points[points.length - 1].ts).getTime();
  const max = Math.max(1, ...series.flatMap(s => points.map(p => Number(p[s.key]))));
  const x = (ts: string) => P + ((new Date(ts).getTime() - t0) / Math.max(1, t1 - t0)) * (W - 2 * P);
  const y = (v: number) => H - P - (v / max) * (H - 2 * P);
  const fmtT = (ms: number) => new Date(ms).toLocaleString([], { month: 'numeric', day: 'numeric', hour: '2-digit', minute: '2-digit' });
  return (
    <div>
      <svg viewBox={`0 0 ${W} ${H}`} className="w-full h-[140px]">
        <line x1={P} y1={H - P} x2={W - P} y2={H - P} className="stroke-border" />
        <text x={2} y={y(max) + 4} className="fill-muted-foreground text-[10px]">{series[0].fmt ? series[0].fmt(max) : max}</text>
        {series.map(s => (
          <polyline key={String(s.key)} fill="none" stroke={s.color} strokeWidth={1.75}
            points={points.map(p => `${x(p.ts)},${y(Number(p[s.key]))}`).join(' ')} />
        ))}
        <text x={P} y={H - 6} className="fill-muted-foreground text-[10px]">{fmtT(t0)}</text>
        <text x={W - P} y={H - 6} textAnchor="end" className="fill-muted-foreground text-[10px]">{fmtT(t1)}</text>
      </svg>
      <div className="flex gap-4 text-xs text-muted-foreground">
        {series.map(s => <span key={String(s.key)} className="flex items-center gap-1"><span className="inline-block w-3 h-0.5" style={{ background: s.color }} />{s.label} (peak per bucket)</span>)}
      </div>
    </div>
  );
};

const Stat: React.FC<{ icon: React.ReactNode; label: string; value: React.ReactNode; sub?: string }> = ({ icon, label, value, sub }) => (
  <Card><CardContent className="p-4">
    <div className="flex items-center gap-2 text-xs text-muted-foreground">{icon}{label}</div>
    <div className="text-2xl font-semibold mt-1">{value}</div>
    {sub && <div className="text-xs text-muted-foreground mt-0.5">{sub}</div>}
  </CardContent></Card>
);

const UsagePage: React.FC = () => {
  const [range, setRange] = useState<'24h' | '7d'>('24h');
  const { data, isLoading } = useQuery<UsageData>({
    queryKey: ['usage', range],
    queryFn: async () => (await axios.get(`/api/v1/admin/usage?range=${range}`)).data,
    refetchInterval: 30000,
  });
  if (isLoading || !data) return <div className="p-6 text-muted-foreground">Loading usage…</div>;
  const { now, series, engagement: e, users } = data;
  const flagged = users.filter(u => u.flags.length > 0);

  return (
    <div className="space-y-6">
      <div className="flex items-center justify-between">
        <h1 className="text-2xl font-bold flex items-center gap-2"><Activity className="h-6 w-6" /> Usage</h1>
        <span className="text-xs text-muted-foreground">Refreshes every 30s · online = active in the last 5 min · days in UTC</span>
      </div>

      <div className="grid grid-cols-2 lg:grid-cols-4 gap-4">
        <Stat icon={<UsersIcon className="size-3.5" />} label="Online now" value={now.online} sub={`${users.length} accounts`} />
        <Stat icon={<Server className="size-3.5" />} label="Kernels running" value={now.kernels} />
        <Stat icon={<Cpu className="size-3.5" />} label="CPU in use" value={`${now.cpu_cores.toFixed(2)} cores`} />
        <Stat icon={<MemoryStick className="size-3.5" />} label="RAM in use" value={`${gb(now.mem_bytes)} GB`} />
      </div>

      <Card><CardContent className="p-4 space-y-4">
        <div className="flex items-center justify-between">
          <div className="text-sm font-medium">Concurrency</div>
          <div className="flex gap-1 text-xs">
            {(['24h', '7d'] as const).map(r => (
              <button key={r} onClick={() => setRange(r)}
                className={`px-2 py-1 rounded ${range === r ? 'bg-primary text-primary-foreground' : 'hover:bg-muted'}`}>{r}</button>
            ))}
          </div>
        </div>
        <LineChart points={series} series={[
          { key: 'online', label: 'Online users', color: '#0d9488' },
          { key: 'kernels', label: 'Kernels', color: '#f59e0b' },
        ]} />
        <LineChart points={series} series={[
          { key: 'cpu_cores', label: 'CPU cores in use', color: '#6366f1', fmt: v => v.toFixed(1) },
        ]} />
      </CardContent></Card>

      <div className="grid grid-cols-2 lg:grid-cols-5 gap-4">
        <Stat icon={<UsersIcon className="size-3.5" />} label="Trial accounts" value={e.approved} sub={`${e.pending} pending approval`} />
        <Stat icon={<Activity className="size-3.5" />} label="Ran code" value={pct(e.activated, e.approved)} sub={`${e.activated} of ${e.approved} ran ≥1 cell`} />
        <Stat icon={<Activity className="size-3.5" />} label="Came back" value={pct(e.returning, e.approved)} sub={`${e.returning} active on 2+ days`} />
        <Stat icon={<Activity className="size-3.5" />} label="Avg time used" value={`${Math.round(e.avg_active_mins)} min`} sub="per trial account" />
        <Stat icon={<AlertTriangle className="size-3.5" />} label="Flagged" value={flagged.length} sub="possible trial abuse" />
      </div>

      <Card><CardContent className="p-0 overflow-x-auto">
        <table className="w-full text-sm">
          <thead className="bg-muted/50 text-xs">
            <tr>
              <th className="text-left py-3 px-4">User</th>
              <th className="text-left py-3 px-3">Last seen</th>
              <th className="text-left py-3 px-3">Kernel now</th>
              <th className="text-right py-3 px-3">Today</th>
              <th className="text-right py-3 px-3">7d active</th>
              <th className="text-right py-3 px-3">Cells 7d / total</th>
              <th className="text-right py-3 px-3">CPU 7d</th>
              <th className="text-left py-3 px-3">Access</th>
              <th className="text-left py-3 px-3">Flags</th>
            </tr>
          </thead>
          <tbody>
            {users.map(u => (
              <tr key={u.id} className="border-b">
                <td className="py-2.5 px-4">
                  <div className="font-medium flex items-center gap-1.5">
                    <span className={`inline-block size-2 rounded-full ${u.online ? 'bg-emerald-500' : 'bg-muted-foreground/30'}`} title={u.online ? 'online' : 'offline'} />
                    {u.username}
                    {u.role === 'superadmin' && <span className="text-[9px] px-1 rounded bg-amber-500/20 text-amber-600 font-semibold uppercase">super</span>}
                  </div>
                  <div className="text-xs text-muted-foreground">{u.email}</div>
                </td>
                <td className="py-2.5 px-3 text-xs">{u.online ? <span className="text-emerald-600">online</span> : ago(u.last_seen_at)}</td>
                <td className="py-2.5 px-3 text-xs font-mono">{u.kernel_running ? `${u.cpu_now.toFixed(2)} cpu · ${gb(u.mem_now)} GB` : <span className="text-muted-foreground">—</span>}</td>
                <td className="py-2.5 px-3 text-right text-xs">{u.today_active_minutes} min</td>
                <td className="py-2.5 px-3 text-right text-xs">{u.active_days_7d}d · {u.active_minutes_7d} min</td>
                <td className="py-2.5 px-3 text-right text-xs">{u.cells_7d} / {u.cells_total}</td>
                <td className="py-2.5 px-3 text-right text-xs">{u.cpu_core_hours_7d.toFixed(1)} core-h</td>
                <td className="py-2.5 px-3 text-xs font-mono">
                  {u.role === 'superadmin' ? '—' : u.access_expires_at ? <AccessCountdown expiresAt={u.access_expires_at} /> : <span className="text-muted-foreground">no limit</span>}
                </td>
                <td className="py-2.5 px-3">
                  <div className="flex flex-wrap gap-1">
                    {u.flags.map(f => (
                      <span key={f} title={FLAG_INFO[f] || f} className="text-[10px] px-1.5 py-0.5 rounded bg-red-500/15 text-red-600 font-semibold">{f.replace(/_/g, ' ')}</span>
                    ))}
                  </div>
                </td>
              </tr>
            ))}
          </tbody>
        </table>
      </CardContent></Card>
    </div>
  );
};

export default UsagePage;
