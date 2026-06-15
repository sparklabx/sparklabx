import React, { useEffect, useState } from 'react';
import axios from 'axios';
import { toast } from 'sonner';
import { Database, ChevronRight, ChevronDown, Table2, Loader2, RefreshCw, ShieldAlert } from 'lucide-react';

// Trino catalog browser. Lists the catalogs / schemas / tables of the user's
// connected Trino — queried AS the user via their OIDC token by the backend
// (GET /api/v1/trino/metadata). Click a table to copy a ready-to-run
// trino("catalog.schema.table") snippet (mirrors the Files "Copy Path" UX).

type MetaResp = {
    enabled: boolean;
    level?: string;
    items?: string[];
    needs_sso?: boolean;
    sso_expired?: boolean;
};

async function fetchMeta(catalog?: string, schema?: string): Promise<MetaResp> {
    const res = await axios.get<MetaResp>('/api/v1/trino/metadata', { params: { catalog, schema } });
    return res.data;
}

type Status = 'loading' | 'ready' | 'needs_sso' | 'sso_expired' | 'error';

export const SidebarTrino: React.FC = () => {
    const [status, setStatus] = useState<Status>('loading');
    const [catalogs, setCatalogs] = useState<string[]>([]);
    const [openCat, setOpenCat] = useState<Record<string, boolean>>({});
    const [openSchema, setOpenSchema] = useState<Record<string, boolean>>({});
    const [schemas, setSchemas] = useState<Record<string, string[]>>({});
    const [tables, setTables] = useState<Record<string, string[]>>({});
    const [busy, setBusy] = useState<Record<string, boolean>>({});

    const loadCatalogs = async () => {
        setStatus('loading');
        try {
            const d = await fetchMeta();
            if (!d.enabled) { setStatus('error'); return; }
            if (d.sso_expired) { setStatus('sso_expired'); return; }
            if (d.needs_sso) { setStatus('needs_sso'); return; }
            setCatalogs(d.items || []);
            setStatus('ready');
        } catch {
            setStatus('error');
        }
    };
    useEffect(() => { void loadCatalogs(); }, []);

    const toggleCat = async (cat: string) => {
        const next = !openCat[cat];
        setOpenCat(s => ({ ...s, [cat]: next }));
        if (next && schemas[cat] === undefined) {
            setBusy(b => ({ ...b, [cat]: true }));
            try { const d = await fetchMeta(cat); setSchemas(s => ({ ...s, [cat]: d.items || [] })); }
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
            try { const d = await fetchMeta(cat, schema); setTables(t => ({ ...t, [key]: d.items || [] })); }
            catch { toast.error('Failed to load tables'); }
            finally { setBusy(b => ({ ...b, [key]: false })); }
        }
    };

    const copyRef = (cat: string, schema: string, table: string) => {
        const snippet = `trino("${cat}.${schema}.${table}")`;
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

    return (
        <div className="p-2 text-xs">
            <div className="flex items-center justify-between px-1 mb-2">
                <span className="font-semibold text-muted-foreground uppercase">Trino Catalog</span>
                <button className="p-1 rounded hover:bg-muted text-muted-foreground" title="Refresh" onClick={() => void loadCatalogs()}>
                    <RefreshCw className="size-3" />
                </button>
            </div>

            {status === 'loading' && (
                <div className="flex items-center gap-2 p-3 text-muted-foreground"><Loader2 className="size-3 animate-spin" /> Loading…</div>
            )}
            {status === 'needs_sso' && <Hint icon={ShieldAlert} title="Sign in with SSO" sub="Trino browsing uses your SSO identity. Log in via SSO to see your catalogs." />}
            {status === 'sso_expired' && <Hint icon={ShieldAlert} title="SSO session expired" sub="Please log out and log in again to refresh access." />}
            {status === 'error' && (
                <div className="p-3 text-center">
                    <Hint icon={Database} title="Trino unavailable" sub="Couldn't reach Trino. Check the connection and retry." />
                    <button className="mt-1 text-[11px] text-primary hover:underline" onClick={() => void loadCatalogs()}>Retry</button>
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
                                        title={`Copy trino("${cat}.${schema}.${table}")`}
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
