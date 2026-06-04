export const STATUS_COLORS: Record<string, string> = {
  draft: 'bg-slate-400',
  configured: 'bg-blue-500',
  provisioning: 'bg-amber-500 animate-pulse',
  ready: 'bg-cyan-500',
  preparing: 'bg-teal-500 animate-pulse',
  prepared: 'bg-emerald-400',
  running: 'bg-emerald-500',
  ended: 'bg-orange-500',
  destroyed: 'bg-slate-600',
  destroying: 'bg-orange-500 animate-pulse',
  deleting: 'bg-red-500 animate-pulse',
  error: 'bg-red-600',
};

export const statusBadgeClass = (status: string): string =>
  STATUS_COLORS[status] || 'bg-gray-500';
