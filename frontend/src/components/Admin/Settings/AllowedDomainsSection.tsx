import React, { useMemo, useState } from 'react';
import { useQuery, useMutation, useQueryClient } from '@tanstack/react-query';
import axios from 'axios';
import { Button } from '../../ui/button';
import { Card, CardContent } from '../../ui/card';
import { Badge } from '../../ui/badge';
import { Input } from '../../ui/input';
import { Label } from '../../ui/label';
import { Textarea } from '../../ui/textarea';
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from '../../ui/select';
import { Switch } from '../../ui/switch';
import { Dialog, DialogContent, DialogHeader, DialogTitle, DialogFooter } from '../../ui/dialog';
import {
  AlertDialog, AlertDialogAction, AlertDialogCancel, AlertDialogContent,
  AlertDialogDescription, AlertDialogFooter, AlertDialogHeader, AlertDialogTitle,
  AlertDialogTrigger,
} from '../../ui/alert-dialog';
import allowedDomainService, { AllowedRule, RuleType } from '../../../services/allowedDomainService';
import { useCurrentUser } from '@/hooks/useCurrentUser';

const DOMAIN_REGEX = /^[a-z0-9]([a-z0-9-]*[a-z0-9])?(\.[a-z0-9]([a-z0-9-]*[a-z0-9])?)+$/;
const EMAIL_REGEX = /^[a-z0-9._%+-]+@[a-z0-9.-]+\.[a-z]{2,}$/;

const validate = (type: RuleType, raw: string): string | null => {
  const v = raw.trim().toLowerCase().replace(/^@/, '');
  if (!v) return 'Value is required';
  if (type === 'domain' && !DOMAIN_REGEX.test(v)) return 'Invalid domain format (e.g. company.com)';
  if (type === 'exact_email' && !EMAIL_REGEX.test(v)) return 'Invalid email format';
  return null;
};

const AllowedDomainsSection: React.FC = () => {
  const queryClient = useQueryClient();
  const { isSuperAdmin: canEdit } = useCurrentUser();
  const [dialogOpen, setDialogOpen] = useState(false);
  const [ruleType, setRuleType] = useState<RuleType>('domain');
  const [value, setValue] = useState('');
  const [note, setNote] = useState('');
  const [serverError, setServerError] = useState<string | null>(null);
  const [forceCreate, setForceCreate] = useState(false);

  const { data: rules = [], isLoading } = useQuery({
    queryKey: ['allowed-domains'],
    queryFn: allowedDomainService.list,
  });

  const createMutation = useMutation({
    mutationFn: () => allowedDomainService.create({ rule_type: ruleType, value: value.trim().toLowerCase().replace(/^@/, ''), note }, forceCreate),
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: ['allowed-domains'] });
      closeDialog();
    },
    onError: (err: unknown) => {
      const msg = axios.isAxiosError(err) ? (err.response?.data?.error ?? err.message) : 'Failed to create rule';
      setServerError(msg);
      // Detect public-provider warning so UI can show the force checkbox
      if (typeof msg === 'string' && msg.includes('public email provider')) {
        setForceCreate(false); // user must explicitly check
      }
    },
  });

  const updateMutation = useMutation({
    mutationFn: ({ id, enabled }: { id: string; enabled: boolean }) => allowedDomainService.update(id, { enabled }),
    onSuccess: () => queryClient.invalidateQueries({ queryKey: ['allowed-domains'] }),
  });

  const deleteMutation = useMutation({
    mutationFn: allowedDomainService.delete,
    onSuccess: () => queryClient.invalidateQueries({ queryKey: ['allowed-domains'] }),
  });

  const clientError = useMemo(() => (value ? validate(ruleType, value) : null), [ruleType, value]);
  const isPublicProviderError = typeof serverError === 'string' && serverError.includes('public email provider');

  const openCreate = () => {
    setRuleType('domain');
    setValue('');
    setNote('');
    setServerError(null);
    setForceCreate(false);
    setDialogOpen(true);
  };

  const closeDialog = () => {
    setDialogOpen(false);
    setValue('');
    setNote('');
    setServerError(null);
    setForceCreate(false);
  };

  const handleSave = () => {
    setServerError(null);
    if (clientError) return;
    createMutation.mutate();
  };

  if (isLoading) {
    return <div className="flex items-center justify-center h-64">Loading...</div>;
  }

  const enabledCount = rules.filter(r => r.enabled).length;

  return (
    <div className="space-y-4">
      <div className="flex items-center justify-between">
        <div>
          <h2 className="text-lg font-semibold">Allowed Email Domains</h2>
          <p className="text-sm text-muted-foreground">
            Restrict who can sign in via Google or Microsoft. {enabledCount === 0 && 'OAuth login is currently disabled.'}
          </p>
        </div>
        {canEdit && <Button onClick={openCreate}>Add Rule</Button>}
      </div>
      {!canEdit && (
        <div className="text-xs px-3 py-2 bg-muted rounded-md text-muted-foreground">
          View only — only a superadmin can add, toggle, or delete allowlist rules.
        </div>
      )}

      {enabledCount === 0 && rules.length === 0 && (
        <Card>
          <CardContent className="py-6 text-sm text-muted-foreground">
            No rules configured. OAuth login (Google / Microsoft) is blocked for everyone until you add at least one rule. Admins can still sign in via username/password.
          </CardContent>
        </Card>
      )}

      {rules.length > 0 && (
        <Card>
          <CardContent className="p-0">
            <table className="w-full text-sm">
              <thead className="border-b bg-muted/50">
                <tr className="text-left">
                  <th className="px-4 py-2 font-medium">Type</th>
                  <th className="px-4 py-2 font-medium">Value</th>
                  <th className="px-4 py-2 font-medium">Note</th>
                  <th className="px-4 py-2 font-medium">Added</th>
                  <th className="px-4 py-2 font-medium text-center">Enabled</th>
                  <th className="px-4 py-2 font-medium text-right">Actions</th>
                </tr>
              </thead>
              <tbody>
                {rules.map((rule: AllowedRule) => (
                  <tr key={rule.id} className="border-b last:border-0">
                    <td className="px-4 py-3">
                      <Badge variant="outline">{rule.rule_type === 'domain' ? 'Domain' : 'Email'}</Badge>
                    </td>
                    <td className="px-4 py-3 font-mono">{rule.value}</td>
                    <td className="px-4 py-3 text-muted-foreground">{rule.note || '-'}</td>
                    <td className="px-4 py-3 text-muted-foreground text-xs">
                      {new Date(rule.created_at).toLocaleDateString()}
                    </td>
                    <td className="px-4 py-3 text-center">
                      {canEdit ? (
                        <Switch
                          checked={rule.enabled}
                          onCheckedChange={(checked) => updateMutation.mutate({ id: rule.id, enabled: checked })}
                        />
                      ) : (
                        <Badge variant={rule.enabled ? 'default' : 'secondary'}>{rule.enabled ? 'On' : 'Off'}</Badge>
                      )}
                    </td>
                    <td className="px-4 py-3 text-right">
                      {canEdit && (
                      <AlertDialog>
                        <AlertDialogTrigger asChild>
                          <Button variant="ghost" size="sm" className="text-destructive">Delete</Button>
                        </AlertDialogTrigger>
                        <AlertDialogContent>
                          <AlertDialogHeader>
                            <AlertDialogTitle>Delete this rule?</AlertDialogTitle>
                            <AlertDialogDescription>
                              <code className="font-mono">{rule.value}</code> will no longer match incoming logins.
                              Existing user sessions remain valid until their JWT expires.
                            </AlertDialogDescription>
                          </AlertDialogHeader>
                          <AlertDialogFooter>
                            <AlertDialogCancel>Cancel</AlertDialogCancel>
                            <AlertDialogAction onClick={() => deleteMutation.mutate(rule.id)}>Delete</AlertDialogAction>
                          </AlertDialogFooter>
                        </AlertDialogContent>
                      </AlertDialog>
                      )}
                    </td>
                  </tr>
                ))}
              </tbody>
            </table>
          </CardContent>
        </Card>
      )}

      {/* Create Dialog */}
      <Dialog open={dialogOpen} onOpenChange={(open) => { if (!open) closeDialog(); else setDialogOpen(true); }}>
        <DialogContent className="max-w-md">
          <DialogHeader>
            <DialogTitle>Add Allowlist Rule</DialogTitle>
          </DialogHeader>
          <div className="space-y-4 py-2">
            <div className="space-y-2">
              <Label>Rule Type</Label>
              <Select value={ruleType} onValueChange={(v) => { setRuleType(v as RuleType); setServerError(null); setForceCreate(false); }}>
                <SelectTrigger>
                  <SelectValue />
                </SelectTrigger>
                <SelectContent>
                  <SelectItem value="domain">Domain (matches any email at this domain)</SelectItem>
                  <SelectItem value="exact_email">Exact Email (single user)</SelectItem>
                </SelectContent>
              </Select>
            </div>

            <div className="space-y-2">
              <Label>{ruleType === 'domain' ? 'Domain' : 'Email'}</Label>
              <Input
                placeholder={ruleType === 'domain' ? 'e.g. company.com' : 'e.g. user@company.com'}
                value={value}
                onChange={(e) => { setValue(e.target.value); setServerError(null); setForceCreate(false); }}
                className={clientError && value ? 'border-destructive' : ''}
                autoFocus
              />
              {clientError && value && (
                <p className="text-xs text-destructive">{clientError}</p>
              )}
            </div>

            <div className="space-y-2">
              <Label>Note (optional)</Label>
              <Textarea
                placeholder="Why is this domain allowed?"
                value={note}
                onChange={(e) => setNote(e.target.value)}
                rows={2}
              />
            </div>

            {serverError && (
              <div className="p-3 bg-red-50 dark:bg-red-950 rounded text-sm text-red-700 dark:text-red-300 space-y-2">
                <div>{serverError}</div>
                {isPublicProviderError && (
                  <label className="flex items-center gap-2 text-xs">
                    <input
                      type="checkbox"
                      checked={forceCreate}
                      onChange={(e) => setForceCreate(e.target.checked)}
                    />
                    I understand — allow anyway
                  </label>
                )}
              </div>
            )}
          </div>
          <DialogFooter>
            <Button variant="outline" onClick={closeDialog}>Cancel</Button>
            <Button
              onClick={handleSave}
              disabled={
                createMutation.isPending ||
                !value.trim() ||
                !!clientError ||
                (isPublicProviderError && !forceCreate)
              }
            >
              {createMutation.isPending ? 'Saving...' : 'Save'}
            </Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>
    </div>
  );
};

export default AllowedDomainsSection;
