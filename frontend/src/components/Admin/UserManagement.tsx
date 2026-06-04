import React, { useState } from 'react';
import { useQuery, useMutation, useQueryClient } from '@tanstack/react-query';
import axios from 'axios';
import { Button } from '../ui/button';
import { Card, CardContent, CardHeader, CardTitle } from '../ui/card';
import { Badge } from '../ui/badge';
import { Dialog, DialogContent, DialogHeader, DialogTitle, DialogTrigger, DialogFooter } from '../ui/dialog';
import { Input } from '../ui/input';
import { Label } from '../ui/label';
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from '../ui/select';
import { Trash2, Plus, Key, Search, Eye, EyeOff, Users, ShieldCheck, ShieldOff } from 'lucide-react';
import { toast } from 'sonner';
import authService from '../../services/authService';
import { useCurrentUser } from '@/hooks/useCurrentUser';
import { AlertDialog, AlertDialogAction, AlertDialogCancel, AlertDialogContent, AlertDialogDescription, AlertDialogFooter, AlertDialogHeader, AlertDialogTitle, AlertDialogTrigger } from '../ui/alert-dialog';

const PasswordInput: React.FC<{ value: string; onChange: (v: string) => void; placeholder?: string }> = ({ value, onChange, placeholder }) => {
  const [show, setShow] = useState(false);
  return (
    <div className="relative">
      <Input type={show ? 'text' : 'password'} value={value} onChange={e => onChange(e.target.value)} placeholder={placeholder} className="pr-9" />
      <button type="button" onClick={() => setShow(!show)}
        className="absolute right-2 top-1/2 -translate-y-1/2 text-muted-foreground hover:text-foreground">
        {show ? <EyeOff className="size-4" /> : <Eye className="size-4" />}
      </button>
    </div>
  );
};

interface Admin {
  id: string;
  username: string;
  email: string;
  role: string;
  created_at: string;
}

const UserManagement: React.FC = () => {
  const queryClient = useQueryClient();
  const { user: currentUser, isSuperAdmin } = useCurrentUser();
  // notebook-lite: no students — only admin accounts. Tab state kept for
  // backward-compat with existing JSX guards (always 'admins').
  const activeTab: 'admins' = 'admins';
  const [searchQuery, setSearchQuery] = useState('');
  // Promote/demote dialog state — replaces native window.confirm for consistent UI.
  const [roleTarget, setRoleTarget] = useState<{ id: string; username: string; newRole: 'admin' | 'superadmin' } | null>(null);

  // Create admin dialog
  const [createOpen, setCreateOpen] = useState(false);
  const [adminForm, setAdminForm] = useState({ username: '', email: '', password: '', confirmPassword: '' });

  // Reset password dialog
  const [resetOpen, setResetOpen] = useState(false);
  const [resetTarget, setResetTarget] = useState<{ id: string; username: string } | null>(null);
  const [newPassword, setNewPassword] = useState('');
  const [confirmNewPassword, setConfirmNewPassword] = useState('');

  // Fetch admins
  const { data: admins = [], isLoading: adminsLoading } = useQuery<Admin[]>({
    queryKey: ['admins'],
    queryFn: async () => {
      const { data } = await axios.get('/api/v1/admin/users');
      return data;
    },
  });

  // notebook-lite: students removed. Empty stubs for any remaining JSX/utility
  // references; tree-shaken away when unused.

  // Create admin
  const createMutation = useMutation({
    mutationFn: async (form: { username: string; email: string; password: string }) => {
      await axios.post('/api/v1/admin/users', form);
    },
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: ['admins'] });
      setCreateOpen(false);
      setAdminForm({ username: '', email: '', password: '', confirmPassword: '' });
    },
  });

  // Delete admin
  const deleteMutation = useMutation({
    mutationFn: async (id: string) => {
      await axios.delete(`/api/v1/admin/users/${id}`);
    },
    onSuccess: () => queryClient.invalidateQueries({ queryKey: ['admins'] }),
  });

  // Reset password
  const resetMutation = useMutation({
    mutationFn: async ({ id, password }: { id: string; password: string }) => {
      await axios.put(`/api/v1/admin/users/${id}/password`, { password });
    },
    onSuccess: () => {
      setResetOpen(false);
      setNewPassword('');
    },
  });

  const filteredAdmins = admins.filter(a =>
    a.username.toLowerCase().includes(searchQuery.toLowerCase()) ||
    a.email.toLowerCase().includes(searchQuery.toLowerCase())
  );


  return (
    <div className="space-y-6">
      <div className="flex items-center justify-between">
        <h1 className="text-2xl font-bold flex items-center gap-2"><Users className="h-6 w-6" /> User Management</h1>
        {activeTab === 'admins' && isSuperAdmin && (
          <Dialog open={createOpen} onOpenChange={setCreateOpen}>
            <DialogTrigger asChild>
              <Button size="sm"><Plus className="size-4 mr-1" /> Add Admin</Button>
            </DialogTrigger>
            <DialogContent>
              <DialogHeader>
                <DialogTitle>Create Admin</DialogTitle>
              </DialogHeader>
              <div className="space-y-4">
                <div className="space-y-2">
                  <Label>Username</Label>
                  <Input value={adminForm.username} onChange={e => setAdminForm({ ...adminForm, username: e.target.value })} />
                </div>
                <div className="space-y-2">
                  <Label>Email</Label>
                  <Input type="email" value={adminForm.email} onChange={e => setAdminForm({ ...adminForm, email: e.target.value })} />
                </div>
                <div className="space-y-2">
                  <Label>Password</Label>
                  <PasswordInput value={adminForm.password} onChange={v => setAdminForm({ ...adminForm, password: v })} />
                </div>
                <div className="space-y-2">
                  <Label>Confirm Password</Label>
                  <PasswordInput value={adminForm.confirmPassword} onChange={v => setAdminForm({ ...adminForm, confirmPassword: v })} />
                  {adminForm.confirmPassword && adminForm.password !== adminForm.confirmPassword && (
                    <p className="text-xs text-destructive">Passwords do not match</p>
                  )}
                </div>
              </div>
              <DialogFooter>
                <Button variant="outline" onClick={() => setCreateOpen(false)}>Cancel</Button>
                <Button onClick={() => createMutation.mutate(adminForm)} disabled={createMutation.isPending || !adminForm.username || !adminForm.password || adminForm.password !== adminForm.confirmPassword}>
                  Create
                </Button>
              </DialogFooter>
            </DialogContent>
          </Dialog>
        )}
      </div>

      {/* No tabs in lite mode — only admin accounts exist */}

      {/* Search */}
      <div className="relative">
        <Search className="absolute left-3 top-1/2 -translate-y-1/2 size-4 text-muted-foreground" />
        <Input
          placeholder={`Search ${activeTab}...`}
          value={searchQuery}
          onChange={e => setSearchQuery(e.target.value)}
          className="pl-9"
        />
      </div>

      {/* Admin Table */}
      {activeTab === 'admins' && (
        <Card>
          <CardContent className="p-0">
            <table className="w-full text-sm">
              <thead className="bg-muted/50">
                <tr>
                  <th className="text-left py-3 px-4">Username</th>
                  <th className="text-left py-3 px-4">Email</th>
                  <th className="text-left py-3 px-4">Created</th>
                  <th className="text-left py-3 pl-8 px-4">Actions</th>
                </tr>
              </thead>
              <tbody>
                {adminsLoading ? (
                  <tr><td colSpan={4} className="py-8 text-center text-muted-foreground">Loading...</td></tr>
                ) : filteredAdmins.length === 0 ? (
                  <tr><td colSpan={4} className="py-8 text-center text-muted-foreground">No admins found</td></tr>
                ) : filteredAdmins.map(admin => (
                  <tr key={admin.id} className="border-b">
                    <td className="py-3 px-4 font-medium">
                      {admin.username}
                      {admin.role === 'superadmin' && (
                        <span className="ml-1.5 text-[9px] px-1 py-0.5 rounded bg-amber-500/20 text-amber-600 font-semibold uppercase">super</span>
                      )}
                    </td>
                    <td className="py-3 px-4 text-muted-foreground">{admin.email}</td>
                    <td className="py-3 px-4 text-muted-foreground text-xs">{new Date(admin.created_at).toLocaleDateString()}</td>
                    <td className="py-3 px-4">
                      <div className="flex gap-1">
                        {isSuperAdmin && (
                          admin.id !== currentUser?.id ? (
                            <Button size="sm" variant="ghost" className="h-7 w-7 p-0" title={admin.role === 'superadmin' ? 'Demote to admin' : 'Promote to superadmin'}
                              onClick={() => {
                                const newRole = admin.role === 'superadmin' ? 'admin' : 'superadmin';
                                setRoleTarget({ id: admin.id, username: admin.username, newRole });
                              }}>
                              {admin.role === 'superadmin' ? <ShieldOff className="size-3.5 text-amber-600" /> : <ShieldCheck className="size-3.5" />}
                            </Button>
                          ) : <div className="w-7" /> /* spacer for alignment */
                        )}
                        {(isSuperAdmin || admin.id === currentUser?.id) && (
                          <Button size="sm" variant="ghost" className="h-7 w-7 p-0" title="Reset Password"
                            onClick={() => { setResetTarget({ id: admin.id, username: admin.username }); setResetOpen(true); }}>
                            <Key className="size-3.5" />
                          </Button>
                        )}
                        {isSuperAdmin && admin.id !== currentUser?.id && (
                        <AlertDialog>
                          <AlertDialogTrigger asChild>
                            <Button size="sm" variant="outline" className="h-7 text-xs text-destructive hover:text-destructive">
                              <Trash2 className="size-3" />
                            </Button>
                          </AlertDialogTrigger>
                          <AlertDialogContent>
                            <AlertDialogHeader>
                              <AlertDialogTitle>Delete admin {admin.username}?</AlertDialogTitle>
                              <AlertDialogDescription>This action cannot be undone.</AlertDialogDescription>
                            </AlertDialogHeader>
                            <AlertDialogFooter>
                              <AlertDialogCancel>Cancel</AlertDialogCancel>
                              <AlertDialogAction className="bg-destructive text-destructive-foreground hover:bg-destructive/90" onClick={() => deleteMutation.mutate(admin.id)}>Delete</AlertDialogAction>
                            </AlertDialogFooter>
                          </AlertDialogContent>
                        </AlertDialog>
                        )}
                      </div>
                    </td>
                  </tr>
                ))}
              </tbody>
            </table>
          </CardContent>
        </Card>
      )}

      {/* Student Table removed in notebook-lite — kept as comment for diff clarity. */}

      {/* Reset Password Dialog */}
      <Dialog open={resetOpen} onOpenChange={setResetOpen}>
        <DialogContent>
          <DialogHeader>
            <DialogTitle>Reset Password: {resetTarget?.username}</DialogTitle>
          </DialogHeader>
          <div className="space-y-4">
            <div className="space-y-2">
              <Label>New Password</Label>
              <PasswordInput value={newPassword} onChange={setNewPassword} />
            </div>
            <div className="space-y-2">
              <Label>Confirm Password</Label>
              <PasswordInput value={confirmNewPassword} onChange={setConfirmNewPassword} />
              {confirmNewPassword && newPassword !== confirmNewPassword && (
                <p className="text-xs text-destructive">Passwords do not match</p>
              )}
            </div>
          </div>
          <DialogFooter>
            <Button variant="outline" onClick={() => { setResetOpen(false); setNewPassword(''); setConfirmNewPassword(''); }}>Cancel</Button>
            <Button onClick={() => { resetTarget && resetMutation.mutate({ id: resetTarget.id, password: newPassword }); setConfirmNewPassword(''); }}
              disabled={resetMutation.isPending || !newPassword || newPassword !== confirmNewPassword}>
              Reset
            </Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>

      {/* Promote / Demote confirmation — replaces native window.confirm. */}
      <AlertDialog open={!!roleTarget} onOpenChange={(open) => !open && setRoleTarget(null)}>
        <AlertDialogContent>
          <AlertDialogHeader>
            <AlertDialogTitle>
              {roleTarget?.newRole === 'superadmin' ? 'Promote to superadmin' : 'Demote to admin'}: {roleTarget?.username}
            </AlertDialogTitle>
            <AlertDialogDescription>
              {roleTarget?.newRole === 'superadmin'
                ? 'This user will gain full settings access (cloud providers, allowlist, user management).'
                : 'This user will lose access to settings writes. They keep normal admin rights.'}
            </AlertDialogDescription>
          </AlertDialogHeader>
          <AlertDialogFooter>
            <AlertDialogCancel>Cancel</AlertDialogCancel>
            <AlertDialogAction
              onClick={async () => {
                if (!roleTarget) return;
                try {
                  await axios.put(`/api/v1/admin/users/${roleTarget.id}/role`, { role: roleTarget.newRole });
                  queryClient.invalidateQueries({ queryKey: ['admins'] });
                  toast.success(`${roleTarget.username} → ${roleTarget.newRole}`);
                } catch {}
                setRoleTarget(null);
              }}
            >
              Confirm
            </AlertDialogAction>
          </AlertDialogFooter>
        </AlertDialogContent>
      </AlertDialog>
    </div>
  );
};

export default UserManagement;
