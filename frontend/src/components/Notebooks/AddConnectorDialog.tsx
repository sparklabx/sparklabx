import React, { useEffect, useState } from 'react';
import axios from 'axios';
import { toast } from 'sonner';
import {
    Dialog, DialogContent, DialogDescription, DialogFooter, DialogHeader, DialogTitle,
} from '@/components/ui/dialog';
import { Button } from '@/components/ui/button';
import { Label } from '@/components/ui/label';
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from '@/components/ui/select';
import { Loader2, Database, AlertTriangle, Lock, Globe, Plug, CheckCircle2, XCircle } from 'lucide-react';
import { useCurrentUser } from '@/hooks/useCurrentUser';

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
    'broker-mapped': 'Shared username / password',
};

// "My Trino Prod" → "my_trino_prod"
function slugify(s: string): string {
    return s.toLowerCase().replace(/[^a-z0-9]+/g, '_').replace(/^_+|_+$/g, '').slice(0, 64);
}

// Default the id to the connector type so the kernel helper reads cleanly —
// postgres(), mysql(), trino(). Dedupe against existing ids: postgres_2, …
function defaultIdFor(type: string, existing: string[]): string {
    if (!type) return '';
    if (!existing.includes(type)) return type;
    let i = 2;
    while (existing.includes(`${type}_${i}`)) i++;
    return `${type}_${i}`;
}

export const AddConnectorDialog: React.FC<{
    open: boolean;
    onClose: () => void;
    onCreated: () => void;
    existingIds: string[];
    editId?: string | null;
}> = ({ open, onClose, onCreated, existingIds, editId }) => {
    const { isSuperAdmin } = useCurrentUser();
    const editing = !!editId;
    const [types, setTypes] = useState<ConnectorTypeInfo[]>([]);
    const [typeId, setTypeId] = useState('');
    const [label, setLabel] = useState('');
    const [id, setId] = useState('');
    const [idEdited, setIdEdited] = useState(false);
    const [url, setUrl] = useState('');
    const [auth, setAuth] = useState('');
    const [username, setUsername] = useState('');
    const [password, setPassword] = useState('');
    const [hasPassword, setHasPassword] = useState(false);
    const [scope, setScope] = useState<'personal' | 'shared'>('personal');
    const [submitting, setSubmitting] = useState(false);
    const [testing, setTesting] = useState(false);
    const [testResult, setTestResult] = useState<{ ok: boolean; msg: string } | null>(null);

    const type = types.find(t => t.id === typeId);

    useEffect(() => {
        if (!open) return;
        // Reset on open.
        setLabel(''); setId(''); setIdEdited(false); setUrl(''); setUsername(''); setPassword(''); setHasPassword(false); setScope('personal'); setTestResult(null);
        axios.get<{ types?: ConnectorTypeInfo[] }>('/api/v1/connector-types')
            .then(r => {
                const ts = r.data?.types || [];
                setTypes(ts);
                if (editId) {
                    // Edit: prefill from the connector's config (no password).
                    axios.get(`/api/v1/connectors/${editId}`).then(res => {
                        const d = res.data;
                        setTypeId(d.type); setLabel(d.label); setId(d.id); setIdEdited(true);
                        setUrl(d.url); setAuth(d.auth); setUsername(d.username || '');
                        setHasPassword(!!d.has_password); setScope(d.scope === 'shared' ? 'shared' : 'personal');
                    }).catch(() => toast.error('Failed to load connector'));
                    return;
                }
                const first = ts[0];
                if (first) { setTypeId(first.id); setAuth(first.default_auth); setId(defaultIdFor(first.id, existingIds)); }
            })
            .catch(() => toast.error('Failed to load connector types'));
    }, [open, editId]);

    const onPickType = (t: string) => {
        setTypeId(t);
        const info = types.find(x => x.id === t);
        setAuth(info?.default_auth || '');
        // Re-default the id to the new type unless the user typed their own.
        if (!idEdited) setId(defaultIdFor(t, existingIds));
    };

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
            const body = { type: typeId, label: label.trim(), url: url.trim(), auth, username, password, scope };
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
                        <Label className="text-xs">Visibility</Label>
                        <div className="grid grid-cols-2 gap-2">
                            <button type="button" onClick={() => setScope('personal')}
                                className={`flex items-center gap-2 rounded-md border px-2.5 py-2 text-left ${scope === 'personal' ? 'border-primary bg-primary/5' : 'border-input hover:bg-muted/40'}`}>
                                <Lock className="size-3.5 shrink-0 text-muted-foreground" />
                                <span><span className="font-medium">Personal</span><br /><span className="text-[10px] text-muted-foreground">Only you</span></span>
                            </button>
                            <button type="button" disabled={!isSuperAdmin} onClick={() => setScope('shared')}
                                title={isSuperAdmin ? undefined : 'Only a superadmin can create shared sources'}
                                className={`flex items-center gap-2 rounded-md border px-2.5 py-2 text-left disabled:opacity-50 disabled:cursor-not-allowed ${scope === 'shared' ? 'border-primary bg-primary/5' : 'border-input hover:bg-muted/40'}`}>
                                <Globe className="size-3.5 shrink-0 text-muted-foreground" />
                                <span><span className="font-medium">Shared</span><br /><span className="text-[10px] text-muted-foreground">Everyone</span></span>
                            </button>
                        </div>
                        {scope === 'shared' && (
                            <div className="flex items-start gap-2 rounded-md border border-amber-200 dark:border-amber-800 bg-amber-50 dark:bg-amber-950 px-2.5 py-2">
                                <AlertTriangle className="size-3.5 shrink-0 text-amber-600 dark:text-amber-400 mt-0.5" />
                                <p className="text-[11px] text-amber-700 dark:text-amber-300">
                                    Visible to <strong>everyone</strong> in this workspace.
                                    {type?.needs_credentials && ' The username/password below will be usable by all users.'}
                                </p>
                            </div>
                        )}
                    </div>

                    <div className="space-y-1.5">
                        <Label className="text-xs">Name</Label>
                        <input className="flex h-9 w-full rounded-md border border-input bg-background px-2 text-sm"
                            placeholder="Analytics Trino" value={label} onChange={e => setLabel(e.target.value)} />
                    </div>

                    <div className="space-y-1.5">
                        <Label className="text-xs">Id <span className="text-muted-foreground">{editing ? '(fixed — used as the helper name)' : '(used in query("id", …))'}</span></Label>
                        <input className="flex h-9 w-full rounded-md border border-input bg-background px-2 text-sm font-mono disabled:opacity-60"
                            placeholder="analytics_trino" value={id} disabled={editing}
                            onChange={e => { setIdEdited(true); setId(slugify(e.target.value)); }} />
                    </div>

                    <div className="space-y-1.5">
                        <Label className="text-xs">JDBC URL</Label>
                        <input className="flex h-9 w-full rounded-md border border-input bg-background px-2 text-sm font-mono"
                            placeholder={URL_PLACEHOLDER[typeId] || 'jdbc:…'} value={url} onChange={e => setUrl(e.target.value)} />
                    </div>

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

                    {type?.needs_credentials && (
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
                                    placeholder={hasPassword ? '••••••••  (leave blank to keep)' : (editing ? 'no password set' : '')}
                                    value={password} onChange={e => setPassword(e.target.value)} />
                            </div>
                        </div>
                    )}
                    {type?.needs_credentials && (
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
