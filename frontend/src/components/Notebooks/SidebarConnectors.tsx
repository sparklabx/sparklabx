import React, { useCallback, useEffect, useState } from 'react';
import axios from 'axios';
import { toast } from 'sonner';
import { Database, ChevronRight, ChevronDown, Table2, Loader2, RefreshCw, ShieldAlert } from 'lucide-react';

// Generic connector catalog browser. Lists catalogs / schemas / tables of the
// selected connector — queried AS the user via the backend
// (GET /api/v1/connectors/:id/metadata, resolved per the connector's auth
// strategy). Click a table to copy a ready-to-run query("<id>", "…") snippet.
// Replaces the Trino-specific SidebarTrino; driven entirely by the /connectors
// registry so a new connector needs no frontend change.

export type Connector = { id: string; label: string; icon: string; kind: string; auth: string };

type MetaResp = {
    enabled: boolean;
    level?: string;
    items?: string[];
    needs_sso?: boolean;
    sso_expired?: boolean;
};

async function fetchMeta(id: string, catalog?: string, schema?: string): Promise<MetaResp> {
    const res = await axios.get<MetaResp>(`/api/v1/connectors/${id}/metadata`, { params: { catalog, schema } });
    return res.data;
}

type Status = 'loading' | 'ready' | 'needs_sso' | 'sso_expired' | 'error';

export const SidebarConnectors: React.FC<{ connectors: Connector[] }> = ({ connectors }) => {
    const [activeId, setActiveId] = useState<string>(connectors[0]?.id ?? '');
    const [status, setStatus] = useState<Status>('loading');
    const [catalogs, setCatalogs] = useState<string[]>([]);
    const [openCat, setOpenCat] = useState<Record<string, boolean>>({});
    const [openSchema, setOpenSchema] = useState<Record<string, boolean>>({});
    const [schemas, setSchemas] = useState<Record<string, string[]>>({});
    const [tables, setTables] = useState<Record<string, string[]>>({});
    const [busy, setBusy] = useState<Record<string, boolean>>({});

    const loadCatalogs = useCallback(async (id: string) => {
        if (!id) { setStatus('error'); return; }
        setStatus('loading');
        // Reset the tree when switching connectors.
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

    useEffect(() => { void loadCatalogs(activeId); }, [activeId, loadCatalogs]);

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
        const snippet = `query("${activeId}", "${cat}.${schema}.${table}")`;
        navigator.clipboard.writeText(snippet);
        toast.success(`Copied: ${snippet}`);
    };

    const Hint: React.FC<{ icon: React.ElementType; title: string; sub: string }> = ({ icon: Icon, title, sub }) => (
        <div className="p-4 text-center text-muted-foreground">
            <Icon className="size-6 mx-auto mb-2 opacity-60" />
            <p className="text-xs font-medium">{title}</p>
            <p className="text-[11px] mt-1">{sub}</p>
        </div>
    );

    const active = connectors.find(c => c.id === activeId);

    return (
        <div className="p-2 text-xs">
            <div className="flex items-center justify-between px-1 mb-2">
                <span className="font-semibold text-muted-foreground uppercase">Catalog</span>
                <button className="p-1 rounded hover:bg-muted text-muted-foreground" title="Refresh" onClick={() => void loadCatalogs(activeId)}>
                    <RefreshCw className="size-3" />
                </button>
            </div>

            {/* Connector picker — shown only when more than one connector is configured. */}
            {connectors.length > 1 && (
                <select
                    className="w-full mb-2 px-2 py-1 rounded border border-border bg-background text-xs"
                    value={activeId}
                    onChange={e => setActiveId(e.target.value)}
                >
                    {connectors.map(c => <option key={c.id} value={c.id}>{c.label}</option>)}
                </select>
            )}

            {status === 'loading' && (
                <div className="flex items-center gap-2 p-3 text-muted-foreground"><Loader2 className="size-3 animate-spin" /> Loading…</div>
            )}
            {status === 'needs_sso' && <Hint icon={ShieldAlert} title="Sign in with SSO" sub="Catalog browsing uses your SSO identity. Log in via SSO to see your data." />}
            {status === 'sso_expired' && <Hint icon={ShieldAlert} title="SSO session expired" sub="Please log out and log in again to refresh access." />}
            {status === 'error' && (
                <div className="p-3 text-center">
                    <Hint icon={Database} title={`${active?.label ?? 'Connector'} unavailable`} sub="Couldn't reach the connector. Check the connection and retry." />
                    <button className="mt-1 text-[11px] text-primary hover:underline" onClick={() => void loadCatalogs(activeId)}>Retry</button>
                </div>
            )}

            {status === 'ready' && catalogs.length === 0 && (
                <p className="px-2 py-3 text-muted-foreground">No catalogs visible.</p>
            )}

            {status === 'ready' && catalogs.map(cat => (
                <div key={cat}>
                    <div className="flex items-center gap-1 py-1 px-1 rounded hover:bg-muted cursor-pointer" onClick={() => void toggleCat(cat)}>
                        {openCat[cat] ? <ChevronDown className="size-3 shrink-0" /> : <ChevronRight className="size-3 shrink-0" />}
                        <Database className="size-3 shrink-0 text-blue-500" />
                        <span className="truncate">{cat}</span>
                        {busy[cat] && <Loader2 className="size-3 animate-spin ml-auto" />}
                    </div>

                    {openCat[cat] && (schemas[cat] || []).map(schema => {
                        const key = `${cat}/${schema}`;
                        return (
                            <div key={key}>
                                <div className="flex items-center gap-1 py-1 pl-5 pr-1 rounded hover:bg-muted cursor-pointer" onClick={() => void toggleSchema(cat, schema)}>
                                    {openSchema[key] ? <ChevronDown className="size-3 shrink-0" /> : <ChevronRight className="size-3 shrink-0" />}
                                    <span className="truncate text-muted-foreground">{schema}</span>
                                    {busy[key] && <Loader2 className="size-3 animate-spin ml-auto" />}
                                </div>
                                {openSchema[key] && (tables[key] || []).map(table => (
                                    <div
                                        key={`${key}/${table}`}
                                        className="flex items-center gap-1 py-1 pl-10 pr-1 rounded hover:bg-muted cursor-pointer group"
                                        title={`Copy query("${activeId}", "${cat}.${schema}.${table}")`}
                                        onClick={() => copyRef(cat, schema, table)}
                                    >
                                        <Table2 className="size-3 shrink-0 text-emerald-500" />
                                        <span className="truncate">{table}</span>
                                    </div>
                                ))}
                                {openSchema[key] && tables[key]?.length === 0 && (
                                    <p className="pl-10 py-1 text-[11px] text-muted-foreground">No tables</p>
                                )}
                            </div>
                        );
                    })}
                    {openCat[cat] && schemas[cat]?.length === 0 && (
                        <p className="pl-5 py-1 text-[11px] text-muted-foreground">No schemas</p>
                    )}
                </div>
            ))}
        </div>
    );
};
