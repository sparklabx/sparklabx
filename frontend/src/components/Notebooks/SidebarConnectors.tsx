import React, { useCallback, useEffect, useState } from 'react';
import axios from 'axios';
import { toast } from 'sonner';
import { Database, ChevronRight, ChevronDown, Table2, Loader2, RefreshCw, ShieldAlert, Plus, Trash2, Pencil, Terminal, Globe, Lock } from 'lucide-react';
import { TrinoIcon } from './parts/TrinoIcon';
import { PostgresIcon } from './parts/PostgresIcon';
import { MysqlIcon } from './parts/MysqlIcon';
import { AddConnectorDialog } from './AddConnectorDialog';

// Data-source manager. Lists the configured connectors (Trino, Postgres, …) and,
// for browsable ones, a lazy catalog tree of whatever depth the backend reports
// (Trino: catalog→schema→table; SQL DBs: schema→table). Clicking a leaf copies a
// ready-to-run helper snippet. Superadmins add/remove sources. Driven by the
// /connectors registry.

export type Connector = {
    id: string;
    label: string;
    icon: string;
    kind: string;
    auth: string;
    browsable?: boolean;
    deletable?: boolean;
    scope?: 'shared' | 'personal';
};

type MetaResp = { enabled: boolean; level?: string; items?: string[]; needs_sso?: boolean; sso_expired?: boolean };

// Fetch the children at a path. The segments map to the backend's
// ?catalog=&schema=&table= params (deepest case: Trino catalog→schema→table→column).
async function fetchMeta(id: string, path: string[]): Promise<MetaResp> {
    const res = await axios.get<MetaResp>(`/api/v1/connectors/${id}/metadata`, {
        params: { catalog: path[0], schema: path[1], table: path[2] },
    });
    return res.data;
}

// Glyph for a connector kind — each keeps its on-brand monochrome mark
// (currentColor, so it tints muted/primary like the surrounding line icons).
const ConnectorIcon: React.FC<{ kind: string }> = ({ kind }) => {
    if (kind === 'trino') return <TrinoIcon className="h-3.5 w-auto shrink-0" />;
    if (kind === 'postgres') return <PostgresIcon className="size-3.5 shrink-0" />;
    if (kind === 'mysql') return <MysqlIcon className="size-3.5 shrink-0" />;
    return <Database className="size-3.5 shrink-0" />;
};

// One node in the lazy tree. catalog/schema/table nodes expand (load children);
// a table node ALSO copies a helper snippet on click (chevron expands it to
// columns). column nodes are leaves rendered as "name (type)" and copy the bare
// column name.
const MetaNode: React.FC<{ connectorId: string; alias: string; path: string[]; level: string }> = ({ connectorId, alias, path, level }) => {
    const expandable = level !== 'column';
    const copyable = level === 'table';
    const [open, setOpen] = useState(false);
    const [children, setChildren] = useState<{ items: string[]; level: string } | null>(null);
    const [busy, setBusy] = useState(false);
    const raw = path[path.length - 1];
    const colName = raw.split(' (')[0]; // strip the " (type)" suffix for columns
    const indent = { paddingLeft: `${(path.length - 1) * 0.75 + 0.25}rem` };

    const expand = async () => {
        const next = !open;
        setOpen(next);
        if (next && children === null) {
            setBusy(true);
            try { const d = await fetchMeta(connectorId, path); setChildren({ items: d.items || [], level: d.level || 'column' }); }
            catch { toast.error('Failed to load'); }
            finally { setBusy(false); }
        }
    };
    const copySnippet = () => {
        const snippet = `${alias}("${path.join('.')}")`;
        navigator.clipboard.writeText(snippet);
        toast.success(`Copied: ${snippet}`);
    };
    const onRow = () => {
        if (copyable) copySnippet();
        else if (level === 'column') { navigator.clipboard.writeText(colName); toast.success(`Copied: ${colName}`); }
        else void expand();
    };

    return (
        <div>
            <div className="group/row flex items-center gap-1 py-1 pr-1 rounded hover:bg-muted cursor-pointer"
                style={indent}
                title={copyable ? `Copy ${alias}("${path.join('.')}")` : (level === 'column' ? `Copy ${colName}` : undefined)}
                onClick={onRow}>
                {expandable
                    ? <span onClick={(e) => { e.stopPropagation(); void expand(); }} className="shrink-0">
                        {open ? <ChevronDown className="size-3" /> : <ChevronRight className="size-3" />}
                      </span>
                    : <span className="w-3 shrink-0" />}
                {level === 'table'
                    ? <Table2 className="size-3 shrink-0 text-emerald-500" />
                    : (path.length === 1 ? <Database className="size-3 shrink-0 text-blue-500" /> : null)}
                <span className={`truncate ${level === 'schema' || level === 'column' ? 'text-muted-foreground' : ''}`}>{raw}</span>
                {busy && <Loader2 className="size-3 animate-spin ml-auto" />}
            </div>
            {open && children && children.items.map(child => (
                <MetaNode key={child} connectorId={connectorId} alias={alias} path={[...path, child]} level={children.level} />
            ))}
            {open && children && children.items.length === 0 && (
                <p className="py-1 text-[11px] text-muted-foreground" style={{ paddingLeft: `${path.length * 0.75 + 0.25}rem` }}>empty</p>
            )}
        </div>
    );
};

type Status = 'loading' | 'ready' | 'needs_sso' | 'sso_expired' | 'error';

const Hint: React.FC<{ icon: React.ElementType; title: string; sub: string }> = ({ icon: Icon, title, sub }) => (
    <div className="p-4 text-center text-muted-foreground">
        <Icon className="size-6 mx-auto mb-2 opacity-60" />
        <p className="text-xs font-medium">{title}</p>
        <p className="text-[11px] mt-1">{sub}</p>
    </div>
);

// The catalog tree for one browsable connector: loads the root level, then nodes
// expand lazily. Remounted (via key) to refresh or switch connectors.
const MetaTree: React.FC<{ connectorId: string; alias: string; label: string; onRefresh: () => void }> = ({ connectorId, alias, label, onRefresh }) => {
    const [status, setStatus] = useState<Status>('loading');
    const [roots, setRoots] = useState<string[]>([]);
    const [rootLevel, setRootLevel] = useState('table');

    const load = useCallback(async () => {
        setStatus('loading');
        try {
            const d = await fetchMeta(connectorId, []);
            if (!d.enabled) { setStatus('error'); return; }
            if (d.sso_expired) { setStatus('sso_expired'); return; }
            if (d.needs_sso) { setStatus('needs_sso'); return; }
            setRoots(d.items || []); setRootLevel(d.level || 'table'); setStatus('ready');
        } catch { setStatus('error'); }
    }, [connectorId]);
    useEffect(() => { void load(); }, [load]);

    if (status === 'loading') return <div className="flex items-center gap-2 py-2 text-muted-foreground"><Loader2 className="size-3 animate-spin" /> Loading…</div>;
    if (status === 'needs_sso') return <Hint icon={ShieldAlert} title="Sign in with SSO" sub={`${label} browses as your SSO identity. Log in via SSO.`} />;
    if (status === 'sso_expired') return <Hint icon={ShieldAlert} title="SSO session expired" sub="Log out and back in to refresh access." />;
    if (status === 'error') return (
        <div className="py-2 text-center">
            <Hint icon={Database} title={`${label} unavailable`} sub="Couldn't reach the source." />
            <button className="text-[11px] text-primary hover:underline" onClick={onRefresh}>Retry</button>
        </div>
    );
    if (roots.length === 0) return <p className="py-2 text-muted-foreground">Nothing visible.</p>;
    return (
        <>
            {roots.map(r => <MetaNode key={r} connectorId={connectorId} alias={alias} path={[r]} level={rootLevel} />)}
            <p className="mt-2 pt-1.5 border-t border-border/50 text-[10px] text-muted-foreground">
                Click a table to copy <code className="text-[10px]">{`${alias}("…")`}</code>.
            </p>
        </>
    );
};

export const SidebarConnectors: React.FC<{ connectors: Connector[]; onChanged: () => void }> = ({ connectors, onChanged }) => {
    const [activeId, setActiveId] = useState<string>(connectors[0]?.id ?? '');
    const [addOpen, setAddOpen] = useState(false);
    const [editId, setEditId] = useState<string | null>(null);
    const [reloadKey, setReloadKey] = useState(0); // bump to remount the tree (refresh)

    // The notebook helper name: the connector's kind when it's the sole one of
    // that kind (postgres()/trino()), else its id — mirrors the kernel's aliases.
    const aliasFor = (c: Connector) => (connectors.filter(x => x.kind === c.kind).length === 1 ? c.kind : c.id);

    // If the selected connector disappears (deleted), collapse to none — but
    // leave an intentional empty selection (collapsed) alone so toggling works.
    useEffect(() => {
        if (activeId && !connectors.some(c => c.id === activeId)) setActiveId('');
    }, [connectors, activeId]);

    const deleteConnector = async (c: Connector) => {
        if (!window.confirm(`Remove data source "${c.label}"? Notebooks using ${aliasFor(c)}() will stop working.`)) return;
        try {
            await axios.delete(`/api/v1/connectors/${c.id}`);
            toast.success(`Removed "${c.label}"`);
            onChanged();
        } catch (e) {
            const err = e as { response?: { data?: { error?: string } } };
            toast.error(err.response?.data?.error || 'Failed to remove data source');
        }
    };

    return (
        <div className="p-2 text-xs">
            <div className="flex items-center justify-between px-1 mb-2">
                <span className="font-semibold text-muted-foreground uppercase">Data Sources</span>
                <button className="p-1 rounded hover:bg-muted text-muted-foreground" title="Add data source" onClick={() => { setEditId(null); setAddOpen(true); }}>
                    <Plus className="size-3.5" />
                </button>
            </div>

            {connectors.length === 0 && (
                <Hint icon={Database} title="No data sources yet"
                    sub="Click + to connect Trino, PostgreSQL or MySQL." />
            )}

            {connectors.map(c => (
                <div key={c.id}>
                    <div className={`group flex items-center gap-1.5 py-1 px-1 rounded cursor-pointer ${c.id === activeId ? 'bg-primary/10 text-primary' : 'hover:bg-muted'}`}
                        onClick={() => setActiveId(c.id === activeId ? '' : c.id)}>
                        <ConnectorIcon kind={c.kind} />
                        <span className="truncate flex-1">{c.label}</span>
                        {c.id === activeId && c.browsable && (
                            <button className="p-0.5 rounded hover:bg-muted-foreground/10" title="Refresh"
                                onClick={(e) => { e.stopPropagation(); setReloadKey(k => k + 1); }}>
                                <RefreshCw className="size-3" />
                            </button>
                        )}
                        {c.scope === 'shared'
                            ? <Globe className="size-3 shrink-0 text-muted-foreground/70" />
                            : <Lock className="size-3 shrink-0 text-muted-foreground/70" />}
                        {c.deletable && (
                            <button className="p-0.5 rounded opacity-0 group-hover:opacity-100 hover:bg-muted-foreground/10"
                                title="Edit" onClick={(e) => { e.stopPropagation(); setEditId(c.id); setAddOpen(true); }}>
                                <Pencil className="size-3" />
                            </button>
                        )}
                        {c.deletable && (
                            <button className="p-0.5 rounded opacity-0 group-hover:opacity-100 hover:bg-destructive/10 hover:text-destructive"
                                title="Remove" onClick={(e) => { e.stopPropagation(); void deleteConnector(c); }}>
                                <Trash2 className="size-3" />
                            </button>
                        )}
                    </div>

                    {c.id === activeId && (
                        <div className="ml-1 mt-0.5 mb-1 border-l border-border/60 pl-2">
                            {c.browsable
                                ? <MetaTree key={`${c.id}:${reloadKey}`} connectorId={c.id} alias={aliasFor(c)} label={c.label} onRefresh={() => setReloadKey(k => k + 1)} />
                                : (
                                    <div className="py-1 text-[11px] text-muted-foreground">
                                        <p className="flex items-center gap-1"><Terminal className="size-3" /> No catalog browser yet.</p>
                                        <p className="mt-1">Use <code className="text-[11px]">{`${aliasFor(c)}("schema.table")`}</code> or <code className="text-[11px]">{`${aliasFor(c)}("SELECT …")`}</code> in a cell.</p>
                                    </div>
                                )}
                        </div>
                    )}
                </div>
            ))}

            <AddConnectorDialog open={addOpen} onClose={() => { setAddOpen(false); setEditId(null); }} onCreated={onChanged} existingIds={connectors.map(c => c.id)} editId={editId} />
        </div>
    );
};
