import React, { useCallback, useEffect, useState } from 'react';
import axios from 'axios';
import { toast } from 'sonner';
import { Database, ChevronRight, ChevronDown, Table2, Loader2, RefreshCw, ShieldAlert, Plus, Trash2, Terminal } from 'lucide-react';
import { TrinoIcon } from './parts/TrinoIcon';
import { PostgresIcon } from './parts/PostgresIcon';
import { MysqlIcon } from './parts/MysqlIcon';
import { AddConnectorDialog } from './AddConnectorDialog';
import { useCurrentUser } from '@/hooks/useCurrentUser';

// Data-source manager. Lists the configured connectors (Trino, Postgres, …),
// each authenticated as the user via the backend. Browsable connectors (Trino)
// expand into a catalog/schema/table tree; others show a query() hint. Superadmins
// can add/remove sources. Driven entirely by the /connectors registry.

export type Connector = {
    id: string;
    label: string;
    icon: string;
    kind: string;
    auth: string;
    browsable?: boolean;
    deletable?: boolean;
};

type MetaResp = { enabled: boolean; level?: string; items?: string[]; needs_sso?: boolean; sso_expired?: boolean };

async function fetchMeta(id: string, catalog?: string, schema?: string): Promise<MetaResp> {
    const res = await axios.get<MetaResp>(`/api/v1/connectors/${id}/metadata`, { params: { catalog, schema } });
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

type Status = 'loading' | 'ready' | 'needs_sso' | 'sso_expired' | 'error';

export const SidebarConnectors: React.FC<{ connectors: Connector[]; onChanged: () => void }> = ({ connectors, onChanged }) => {
    const { isSuperAdmin } = useCurrentUser();
    const [activeId, setActiveId] = useState<string>(connectors[0]?.id ?? '');
    const [addOpen, setAddOpen] = useState(false);

    const [status, setStatus] = useState<Status>('loading');
    const [catalogs, setCatalogs] = useState<string[]>([]);
    const [openCat, setOpenCat] = useState<Record<string, boolean>>({});
    const [openSchema, setOpenSchema] = useState<Record<string, boolean>>({});
    const [schemas, setSchemas] = useState<Record<string, string[]>>({});
    const [tables, setTables] = useState<Record<string, string[]>>({});
    const [busy, setBusy] = useState<Record<string, boolean>>({});

    const active = connectors.find(c => c.id === activeId);

    // Keep a valid selection as the list changes (add/delete).
    useEffect(() => {
        if (connectors.length && !connectors.some(c => c.id === activeId)) {
            setActiveId(connectors[0].id);
        }
    }, [connectors, activeId]);

    const loadCatalogs = useCallback(async (id: string, browsable?: boolean) => {
        if (!id || !browsable) { setStatus('ready'); return; }
        setStatus('loading');
        setCatalogs([]); setOpenCat({}); setOpenSchema({}); setSchemas({}); setTables({});
        try {
            const d = await fetchMeta(id);
            if (!d.enabled) { setStatus('error'); return; }
            if (d.sso_expired) { setStatus('sso_expired'); return; }
            if (d.needs_sso) { setStatus('needs_sso'); return; }
            setCatalogs(d.items || []);
            setStatus('ready');
        } catch {
            setStatus('error');
        }
    }, []);

    useEffect(() => { void loadCatalogs(activeId, active?.browsable); }, [activeId, active?.browsable, loadCatalogs]);

    const toggleCat = async (cat: string) => {
        const next = !openCat[cat];
        setOpenCat(s => ({ ...s, [cat]: next }));
        if (next && schemas[cat] === undefined) {
            setBusy(b => ({ ...b, [cat]: true }));
            try { const d = await fetchMeta(activeId, cat); setSchemas(s => ({ ...s, [cat]: d.items || [] })); }
            catch { toast.error('Failed to load schemas'); }
            finally { setBusy(b => ({ ...b, [cat]: false })); }
        }
    };

    const toggleSchema = async (cat: string, schema: string) => {
        const key = `${cat}/${schema}`;
        const next = !openSchema[key];
        setOpenSchema(s => ({ ...s, [key]: next }));
        if (next && tables[key] === undefined) {
            setBusy(b => ({ ...b, [key]: true }));
            try { const d = await fetchMeta(activeId, cat, schema); setTables(t => ({ ...t, [key]: d.items || [] })); }
            catch { toast.error('Failed to load tables'); }
            finally { setBusy(b => ({ ...b, [key]: false })); }
        }
    };

    const copyRef = (cat: string, schema: string, table: string) => {
        const snippet = `${activeId}("${cat}.${schema}.${table}")`;
        navigator.clipboard.writeText(snippet);
        toast.success(`Copied: ${snippet}`);
    };

    const deleteConnector = async (c: Connector) => {
        if (!window.confirm(`Remove data source "${c.label}"? Notebooks using ${c.id}() will stop working.`)) return;
        try {
            await axios.delete(`/api/v1/connectors/${c.id}`);
            toast.success(`Removed "${c.label}"`);
            onChanged();
        } catch (e) {
            const err = e as { response?: { data?: { error?: string } } };
            toast.error(err.response?.data?.error || 'Failed to remove data source');
        }
    };

    const Hint: React.FC<{ icon: React.ElementType; title: string; sub: string }> = ({ icon: Icon, title, sub }) => (
        <div className="p-4 text-center text-muted-foreground">
            <Icon className="size-6 mx-auto mb-2 opacity-60" />
            <p className="text-xs font-medium">{title}</p>
            <p className="text-[11px] mt-1">{sub}</p>
        </div>
    );

    return (
        <div className="p-2 text-xs">
            <div className="flex items-center justify-between px-1 mb-2">
                <span className="font-semibold text-muted-foreground uppercase">Data Sources</span>
                {isSuperAdmin && (
                    <button className="p-1 rounded hover:bg-muted text-muted-foreground" title="Add data source" onClick={() => setAddOpen(true)}>
                        <Plus className="size-3.5" />
                    </button>
                )}
            </div>

            {connectors.length === 0 && (
                <Hint
                    icon={Database}
                    title="No data sources yet"
                    sub={isSuperAdmin ? 'Click + to connect Trino, PostgreSQL or MySQL.' : 'Ask a superadmin to add a data source.'}
                />
            )}

            {/* Connector list — click to select; trash to remove (superadmin). */}
            {connectors.map(c => (
                <div key={c.id}>
                    <div
                        className={`group flex items-center gap-1.5 py-1 px-1 rounded cursor-pointer ${c.id === activeId ? 'bg-primary/10 text-primary' : 'hover:bg-muted'}`}
                        onClick={() => setActiveId(c.id)}
                    >
                        <ConnectorIcon kind={c.kind} />
                        <span className="truncate flex-1">{c.label}</span>
                        {c.id === activeId && active?.browsable && (
                            <button className="p-0.5 rounded hover:bg-muted-foreground/10" title="Refresh"
                                onClick={(e) => { e.stopPropagation(); void loadCatalogs(c.id, true); }}>
                                <RefreshCw className="size-3" />
                            </button>
                        )}
                        {isSuperAdmin && c.deletable && (
                            <button className="p-0.5 rounded opacity-0 group-hover:opacity-100 hover:bg-destructive/10 hover:text-destructive"
                                title="Remove" onClick={(e) => { e.stopPropagation(); void deleteConnector(c); }}>
                                <Trash2 className="size-3" />
                            </button>
                        )}
                    </div>

                    {/* Detail for the selected connector. */}
                    {c.id === activeId && (
                        <div className="ml-1 mt-0.5 mb-1 border-l border-border/60 pl-2">
                            {!c.browsable ? (
                                <div className="py-1 text-[11px] text-muted-foreground">
                                    <p className="flex items-center gap-1"><Terminal className="size-3" /> No catalog browser yet.</p>
                                    <p className="mt-1">Use <code className="text-[11px]">{`${c.id}("schema.table")`}</code> or <code className="text-[11px]">{`query("${c.id}", "SELECT …")`}</code> in a cell.</p>
                                </div>
                            ) : (
                                <>
                                    {status === 'loading' && <div className="flex items-center gap-2 py-2 text-muted-foreground"><Loader2 className="size-3 animate-spin" /> Loading…</div>}
                                    {status === 'needs_sso' && <Hint icon={ShieldAlert} title="Sign in with SSO" sub={`${c.label} browses as your SSO identity. Log in via SSO.`} />}
                                    {status === 'sso_expired' && <Hint icon={ShieldAlert} title="SSO session expired" sub="Log out and back in to refresh access." />}
                                    {status === 'error' && (
                                        <div className="py-2 text-center">
                                            <Hint icon={Database} title={`${c.label} unavailable`} sub="Couldn't reach the source." />
                                            <button className="text-[11px] text-primary hover:underline" onClick={() => void loadCatalogs(c.id, true)}>Retry</button>
                                        </div>
                                    )}
                                    {status === 'ready' && catalogs.length === 0 && <p className="py-2 text-muted-foreground">No catalogs visible.</p>}
                                    {status === 'ready' && catalogs.map(cat => (
                                        <div key={cat}>
                                            <div className="flex items-center gap-1 py-1 rounded hover:bg-muted cursor-pointer" onClick={() => void toggleCat(cat)}>
                                                {openCat[cat] ? <ChevronDown className="size-3 shrink-0" /> : <ChevronRight className="size-3 shrink-0" />}
                                                <Database className="size-3 shrink-0 text-blue-500" />
                                                <span className="truncate">{cat}</span>
                                                {busy[cat] && <Loader2 className="size-3 animate-spin ml-auto" />}
                                            </div>
                                            {openCat[cat] && (schemas[cat] || []).map(schema => {
                                                const key = `${cat}/${schema}`;
                                                return (
                                                    <div key={key}>
                                                        <div className="flex items-center gap-1 py-1 pl-4 rounded hover:bg-muted cursor-pointer" onClick={() => void toggleSchema(cat, schema)}>
                                                            {openSchema[key] ? <ChevronDown className="size-3 shrink-0" /> : <ChevronRight className="size-3 shrink-0" />}
                                                            <span className="truncate text-muted-foreground">{schema}</span>
                                                            {busy[key] && <Loader2 className="size-3 animate-spin ml-auto" />}
                                                        </div>
                                                        {openSchema[key] && (tables[key] || []).map(table => (
                                                            <div key={`${key}/${table}`}
                                                                className="flex items-center gap-1 py-1 pl-8 rounded hover:bg-muted cursor-pointer"
                                                                title={`Copy ${c.id}("${cat}.${schema}.${table}")`}
                                                                onClick={() => copyRef(cat, schema, table)}>
                                                                <Table2 className="size-3 shrink-0 text-emerald-500" />
                                                                <span className="truncate">{table}</span>
                                                            </div>
                                                        ))}
                                                        {openSchema[key] && tables[key]?.length === 0 && <p className="pl-8 py-1 text-[11px] text-muted-foreground">No tables</p>}
                                                    </div>
                                                );
                                            })}
                                            {openCat[cat] && schemas[cat]?.length === 0 && <p className="pl-4 py-1 text-[11px] text-muted-foreground">No schemas</p>}
                                        </div>
                                    ))}
                                    {status === 'ready' && catalogs.length > 0 && (
                                        <p className="mt-2 pt-1.5 border-t border-border/50 text-[10px] text-muted-foreground">
                                            Click a table to copy <code className="text-[10px]">{`${c.id}("…")`}</code>.
                                        </p>
                                    )}
                                </>
                            )}
                        </div>
                    )}
                </div>
            ))}

            <AddConnectorDialog open={addOpen} onClose={() => setAddOpen(false)} onCreated={onChanged} />
        </div>
    );
};
