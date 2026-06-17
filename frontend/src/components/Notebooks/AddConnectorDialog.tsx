import React, { useEffect, useState } from 'react';
import axios from 'axios';
import { toast } from 'sonner';
import {
    Dialog, DialogContent, DialogDescription, DialogFooter, DialogHeader, DialogTitle,
} from '@/components/ui/dialog';
import { Button } from '@/components/ui/button';
import { Label } from '@/components/ui/label';
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from '@/components/ui/select';
import { Switch } from '@/components/ui/switch';
import { Loader2, Database, Plug, CheckCircle2, XCircle } from 'lucide-react';

// Dialog for adding a data connector (superadmin). Driven by /connector-types so
// new backend types appear here with no frontend change.

type ConnectorTypeInfo = {
    id: string;
    label: string;
    icon: string;
    browsable: boolean;
    needs_credentials: boolean;
    auth_options: string[];
    default_auth: string;
    driver_package: string;
};

const URL_PLACEHOLDER: Record<string, string> = {
    trino: 'jdbc:trino://trino.corp:443?SSL=true',
    postgres: 'jdbc:postgresql://db.corp:5432/analytics',
    mysql: 'jdbc:mysql://db.corp:3306/analytics',
};

const AUTH_LABEL: Record<string, string> = {
    'app-jwt': 'App-signed JWT (any login works)',
    'idp-passthrough': 'Forward IdP token (SSO only)',
    'broker-mapped': 'Username / password',
};

// "Trino Staging" → "trino_staging" — the notebook helper id is derived from the
// name the user types, so it's meaningful (query("trino_staging", …)).
function slugify(s: string): string {
    return s.toLowerCase().replace(/[^a-z0-9]+/g, '_').replace(/^_+|_+$/g, '').slice(0, 64);
}

// Make a base id unique against the existing ones: trino, then trino_2, …
function uniqueId(base: string, existing: string[]): string {
    if (!base) return '';
    if (!existing.includes(base)) return base;
    let i = 2;
    while (existing.includes(`${base}_${i}`)) i++;
    return `${base}_${i}`;
}

// Per-type connection defaults for the structured (host/port/db/ssl) form.
const CONN_META: Record<string, { port: string; needsDb: boolean; dbLabel: string }> = {
    trino: { port: '8080', needsDb: false, dbLabel: 'Catalog' },
    postgres: { port: '5432', needsDb: true, dbLabel: 'Database' },
    mysql: { port: '3306', needsDb: true, dbLabel: 'Database' },
};

// Assemble a JDBC URL from the structured fields, per connector type.
function buildJdbcUrl(type: string, host: string, port: string, db: string, ssl: boolean): string {
    const h = host.trim();
    if (!h) return '';
    const hp = port.trim() ? `${h}:${port.trim()}` : h;
    const d = db.trim();
    switch (type) {
        case 'trino':
            return `jdbc:trino://${hp}` + (ssl ? '?SSL=true&SSLVerification=NONE' : '');
        case 'postgres':
            return `jdbc:postgresql://${hp}/${d}` + (ssl ? '?sslmode=require' : '');
        case 'mysql':
            return `jdbc:mysql://${hp}/${d}` + (ssl ? '?useSSL=true&requireSSL=true' : '');
        default:
            return `jdbc:${type}://${hp}/${d}`;
    }
}

// Parse a stored JDBC URL back into structured fields. Returns null when the URL
// carries params we don't model — the caller then falls back to the raw field so
// nothing is silently dropped.
function parseJdbcUrl(raw: string): { host: string; port: string; db: string; ssl: boolean } | null {
    const m = /^jdbc:\w+:\/\/([^:/?]+)(?::(\d+))?(?:\/([^?]*))?(?:\?(.*))?$/.exec(raw.trim());
    if (!m) return null;
    const allowed = new Set(['ssl', 'sslverification', 'sslmode', 'usessl', 'requiressl']);
    for (const kv of (m[4] || '').split('&').filter(Boolean)) {
        if (!allowed.has(kv.split('=')[0].toLowerCase())) return null; // exotic param → keep raw
    }
    const q = (m[4] || '').toLowerCase();
    const ssl = q.includes('ssl=true') || q.includes('sslmode=require') || q.includes('usessl=true');
    return { host: m[1], port: m[2] || '', db: m[3] || '', ssl };
}

export const AddConnectorDialog: React.FC<{
    open: boolean;
    onClose: () => void;
    onCreated: () => void;
    existingIds: string[];
    editId?: string | null;
}> = ({ open, onClose, onCreated, existingIds, editId }) => {
    const editing = !!editId;
    const [types, setTypes] = useState<ConnectorTypeInfo[]>([]);
    const [typeId, setTypeId] = useState('');
    const [label, setLabel] = useState('');
    const [id, setId] = useState('');
    const [url, setUrl] = useState('');
    const [host, setHost] = useState('');
    const [port, setPort] = useState('');
    const [database, setDatabase] = useState('');
    const [ssl, setSsl] = useState(false);
    const [advanced, setAdvanced] = useState(false); // true → edit the raw JDBC URL
    const [auth, setAuth] = useState('');
    const [username, setUsername] = useState('');
    const [password, setPassword] = useState('');
    const [hasPassword, setHasPassword] = useState(false);
    const [submitting, setSubmitting] = useState(false);
    const [testing, setTesting] = useState(false);
    const [testResult, setTestResult] = useState<{ ok: boolean; msg: string } | null>(null);

    const type = types.find(t => t.id === typeId);

    useEffect(() => {
        if (!open) return;
        // Reset on open.
        setLabel(''); setId(''); setUrl(''); setUsername(''); setPassword(''); setHasPassword(false); setTestResult(null);
        setHost(''); setPort(''); setDatabase(''); setSsl(false); setAdvanced(false);
        axios.get<{ types?: ConnectorTypeInfo[] }>('/api/v1/connector-types')
            .then(r => {
                const ts = r.data?.types || [];
                setTypes(ts);
                if (editId) {
                    // Edit: prefill from the connector's config (no password).
                    axios.get(`/api/v1/connectors/${editId}`).then(res => {
                        const d = res.data;
                        setTypeId(d.type); setLabel(d.label); setId(d.id);
                        setUrl(d.url); setAuth(d.auth); setUsername(d.username || '');
                        setHasPassword(!!d.has_password);
                        // Parse the stored URL into host/port/db/ssl; fall back to raw on exotic URLs.
                        const parsed = parseJdbcUrl(d.url);
                        if (parsed) { setHost(parsed.host); setPort(parsed.port); setDatabase(parsed.db); setSsl(parsed.ssl); }
                        else { setAdvanced(true); }
                    }).catch(() => toast.error('Failed to load connector'));
                    return;
                }
                const first = ts[0];
                if (first) { setTypeId(first.id); setAuth(first.default_auth); setPort(CONN_META[first.id]?.port || ''); }
            })
            .catch(() => toast.error('Failed to load connector types'));
    }, [open, editId]);

    const onPickType = (t: string) => {
        setTypeId(t);
        const info = types.find(x => x.id === t);
        setAuth(info?.default_auth || '');
        setPort(CONN_META[t]?.port || '');
    };

    // Derive the notebook id from the name (falling back to the type), deduped —
    // so it's meaningful ("Trino Staging" → trino_staging) instead of trino_2.
    useEffect(() => {
        if (editing) return;
        setId(uniqueId(slugify(label) || typeId, existingIds));
    }, [editing, label, typeId, existingIds]);

    // Keep the JDBC URL derived from the structured fields (unless the user
    // switched to Advanced and is editing the raw URL directly).
    useEffect(() => {
        if (advanced) return;
        setUrl(buildJdbcUrl(typeId, host, port, database, ssl));
    }, [advanced, typeId, host, port, database, ssl]);

    const testConnection = async () => {
        if (!typeId || !url.trim()) { toast.error('Pick a type and enter a URL first'); return; }
        setTesting(true); setTestResult(null);
        try {
            const r = await axios.post<{ ok?: boolean; message?: string; error?: string }>('/api/v1/connectors/test', {
                id: editId || undefined, type: typeId, url: url.trim(), auth, username, password,
            });
            setTestResult(r.data?.ok
                ? { ok: true, msg: r.data.message || 'Connected' }
                : { ok: false, msg: r.data?.error || 'Connection failed' });
        } catch (e) {
            const err = e as { response?: { data?: { error?: string } } };
            setTestResult({ ok: false, msg: err.response?.data?.error || 'Connection failed' });
        } finally {
            setTesting(false);
        }
    };

    const submit = async () => {
        if (!typeId || !label.trim() || !id.trim() || !url.trim()) {
            toast.error('Type, label, id and URL are required');
            return;
        }
        setSubmitting(true);
        try {
            const body = { type: typeId, label: label.trim(), url: url.trim(), auth, username, password };
            if (editing) {
                await axios.put(`/api/v1/connectors/${editId}`, body);
                toast.success(`Saved "${label.trim()}"`);
            } else {
                await axios.post('/api/v1/connectors', { id: id.trim(), ...body });
                toast.success(`Added data source "${label.trim()}"`);
            }
            onCreated();
            onClose();
        } catch (e) {
            const err = e as { response?: { data?: { error?: string } } };
            toast.error(err.response?.data?.error || (editing ? 'Failed to save' : 'Failed to add data source'));
        } finally {
            setSubmitting(false);
        }
    };

    return (
        <Dialog open={open} onOpenChange={(o) => !o && onClose()}>
            <DialogContent className="max-w-md">
                <DialogHeader>
                    <DialogTitle className="flex items-center gap-2">
                        <Database className="size-4 text-muted-foreground" /> {editing ? 'Edit data source' : 'Add data source'}
                    </DialogTitle>
                    <DialogDescription>Connect a Trino, PostgreSQL or MySQL source for notebooks.</DialogDescription>
                </DialogHeader>

                <div className="space-y-3 text-sm">
                    <div className="space-y-1.5">
                        <Label className="text-xs">Type</Label>
                        <Select value={typeId} onValueChange={onPickType} disabled={editing}>
                            <SelectTrigger className="h-9"><SelectValue placeholder="Select a type…" /></SelectTrigger>
                            <SelectContent>
                                {types.map(t => <SelectItem key={t.id} value={t.id}>{t.label}</SelectItem>)}
                            </SelectContent>
                        </Select>
                    </div>

                    <div className="space-y-1.5">
                        <Label className="text-xs">Name</Label>
                        <input className="flex h-9 w-full rounded-md border border-input bg-background px-2 text-sm"
                            value={label} onChange={e => setLabel(e.target.value)} />
                    </div>

                    {advanced ? (
                        <div className="space-y-1.5">
                            <div className="flex items-center justify-between">
                                <Label className="text-xs">JDBC URL</Label>
                                <button type="button" className="text-[11px] text-primary hover:underline"
                                    onClick={() => setAdvanced(false)}>Use host / port fields</button>
                            </div>
                            <input className="flex h-9 w-full rounded-md border border-input bg-background px-2 text-sm font-mono"
                                placeholder={URL_PLACEHOLDER[typeId] || 'jdbc:…'} value={url} onChange={e => setUrl(e.target.value)} />
                        </div>
                    ) : (
                        <div className="space-y-2">
                            <div className="grid grid-cols-[1fr_88px] gap-2">
                                <div className="space-y-1.5">
                                    <Label className="text-xs">Host</Label>
                                    <input className="flex h-9 w-full rounded-md border border-input bg-background px-2 text-sm"
                                        value={host} onChange={e => setHost(e.target.value)} />
                                </div>
                                <div className="space-y-1.5">
                                    <Label className="text-xs">Port</Label>
                                    <input className="flex h-9 w-full rounded-md border border-input bg-background px-2 text-sm font-mono"
                                        inputMode="numeric" placeholder={CONN_META[typeId]?.port || ''} value={port} onChange={e => setPort(e.target.value.replace(/[^0-9]/g, ''))} />
                                </div>
                            </div>
                            {CONN_META[typeId]?.needsDb && (
                                <div className="space-y-1.5">
                                    <Label className="text-xs">{CONN_META[typeId]?.dbLabel || 'Database'}</Label>
                                    <input className="flex h-9 w-full rounded-md border border-input bg-background px-2 text-sm"
                                        value={database} onChange={e => setDatabase(e.target.value)} />
                                </div>
                            )}
                            <div className="flex items-center justify-between">
                                <label className="flex items-center gap-2 text-xs cursor-pointer">
                                    <Switch checked={ssl} onCheckedChange={setSsl} /> Use SSL/TLS
                                </label>
                                <button type="button" className="text-[11px] text-primary hover:underline"
                                    onClick={() => setAdvanced(true)}>Advanced: JDBC URL</button>
                            </div>
                            {url && (
                                <p className="text-[11px] text-muted-foreground font-mono break-all">{url}</p>
                            )}
                        </div>
                    )}

                    {type && type.auth_options.length > 1 && (
                        <div className="space-y-1.5">
                            <Label className="text-xs">Authentication</Label>
                            <Select value={auth} onValueChange={setAuth}>
                                <SelectTrigger className="h-9"><SelectValue /></SelectTrigger>
                                <SelectContent>
                                    {type.auth_options.map(a => <SelectItem key={a} value={a}>{AUTH_LABEL[a] || a}</SelectItem>)}
                                </SelectContent>
                            </Select>
                        </div>
                    )}

                    {auth === 'broker-mapped' && (
                        <div className="grid grid-cols-2 gap-2">
                            <div className="space-y-1.5">
                                <Label className="text-xs">Username</Label>
                                <input className="flex h-9 w-full rounded-md border border-input bg-background px-2 text-sm"
                                    value={username} onChange={e => setUsername(e.target.value)} />
                            </div>
                            <div className="space-y-1.5">
                                <Label className="text-xs flex items-center gap-1.5">
                                    Password
                                    {editing && (hasPassword
                                        ? <span className="text-[10px] font-normal text-emerald-600 dark:text-emerald-400">• set</span>
                                        : <span className="text-[10px] font-normal text-muted-foreground">• none</span>)}
                                </Label>
                                <input type="password" className="flex h-9 w-full rounded-md border border-input bg-background px-2 text-sm"
                                    placeholder={hasPassword ? '••••••••' : ''}
                                    value={password} onChange={e => setPassword(e.target.value)} />
                            </div>
                        </div>
                    )}
                    {auth === 'broker-mapped' && (
                        <p className="text-[11px] text-muted-foreground">Credentials are stored encrypted and shared by everyone using this source.</p>
                    )}
                </div>

                {testResult && (
                    <p className={`flex items-center gap-1.5 text-xs ${testResult.ok ? 'text-emerald-600 dark:text-emerald-400' : 'text-destructive'}`}>
                        {testResult.ok ? <CheckCircle2 className="size-3.5 shrink-0" /> : <XCircle className="size-3.5 shrink-0" />}
                        <span className="break-all">{testResult.msg}</span>
                    </p>
                )}
                <DialogFooter className="gap-2 sm:justify-between">
                    <Button variant="outline" size="sm" onClick={testConnection} disabled={testing} className="h-8 px-2.5 text-xs sm:mr-auto">
                        {testing ? <Loader2 className="mr-1.5 size-3.5 animate-spin" /> : <Plug className="mr-1.5 size-3.5" />}
                        Test connection
                    </Button>
                    <div className="flex gap-2">
                        <Button variant="outline" onClick={onClose}>Cancel</Button>
                        <Button onClick={submit} disabled={submitting}>
                            {submitting && <Loader2 className="mr-2 size-4 animate-spin" />}
                            {editing ? 'Save' : 'Add source'}
                        </Button>
                    </div>
                </DialogFooter>
            </DialogContent>
        </Dialog>
    );
};
