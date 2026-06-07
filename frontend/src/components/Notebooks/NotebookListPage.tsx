/**
 * NotebookListPage - List and create notebooks
 */
'use client';

import React, { useState } from 'react';
import { useNavigate } from 'react-router-dom';
import { Button } from '@/components/ui/button';
import { Input } from '@/components/ui/input';
import {
    Card,
    CardContent,
    CardDescription,
    CardHeader,
    CardTitle,
} from '@/components/ui/card';
import {
    Dialog,
    DialogContent,
    DialogDescription,
    DialogHeader,
    DialogTitle,
    DialogTrigger,
    DialogFooter,
} from '@/components/ui/dialog';
import {
    Select,
    SelectContent,
    SelectItem,
    SelectTrigger,
    SelectValue,
} from '@/components/ui/select';
import { Plus, FileCode, Trash2, Loader2, Clock, Notebook as NotebookIcon, MoreVertical, Pencil } from 'lucide-react';
import {
    DropdownMenu, DropdownMenuContent, DropdownMenuItem, DropdownMenuTrigger,
} from '@/components/ui/dropdown-menu';
import { AlertDialog, AlertDialogAction, AlertDialogCancel, AlertDialogContent, AlertDialogDescription, AlertDialogFooter, AlertDialogHeader, AlertDialogTitle } from '@/components/ui/alert-dialog';

import { useNotebookList } from '@/hooks/useNotebook';
import { NotebookLanguage } from '@/services/notebookService';

export default function NotebookListPage() {
    const navigate = useNavigate();
    const { notebooks, loading, loadNotebooks, createNotebook, deleteNotebook } = useNotebookList();

    // Auto-redirect to last opened notebook
    React.useEffect(() => {
        if (!loading && notebooks.length > 0) {
            const lastId = localStorage.getItem('sparklabx-last-notebook');
            if (lastId && notebooks.some(n => n.id === lastId)) {
                navigate(`/notebooks/${lastId}`, { replace: true });
            }
        }
    }, [loading, notebooks, navigate]);

    // Create dialog state
    const [isCreateOpen, setIsCreateOpen] = useState(false);
    const [newName, setNewName] = useState('Untitled Notebook');
    const [newLanguage, setNewLanguage] = useState<NotebookLanguage>('python');
    const [isCreating, setIsCreating] = useState(false);

    const handleCreate = async () => {
        setIsCreating(true);
        const notebook = await createNotebook({
            name: newName,
            language: newLanguage,
        });
        setIsCreating(false);
        setIsCreateOpen(false);

        if (notebook) {
            navigate(`/notebooks/${notebook.id}`);
        }
    };

    const [deleteTarget, setDeleteTarget] = useState<{ id: string; name: string } | null>(null);
    const handleDelete = (id: string, name: string) => {
        setDeleteTarget({ id, name });
    };

    const [renameTarget, setRenameTarget] = useState<{ id: string; name: string } | null>(null);
    const [renameValue, setRenameValue] = useState('');
    const handleRename = (id: string, currentName: string) => {
        setRenameTarget({ id, name: currentName });
        setRenameValue(currentName);
    };
    const confirmRename = async () => {
        if (!renameTarget || !renameValue.trim()) return;
        try {
            await import('@/services/notebookService').then(m => m.notebookService.updateNotebook(renameTarget.id, { name: renameValue.trim() }));
            loadNotebooks();
        } catch { /* ignore — toast handled in axios interceptor */ }
        setRenameTarget(null);
    };

    const handleOpen = (id: string) => {
        localStorage.setItem('sparklabx-last-notebook', id);
        navigate(`/notebooks/${id}`);
    };

    return (
        <div className="space-y-6">
            {/* Header */}
            <div className="flex items-center justify-between">
                <div>
                    <h1 className="text-2xl font-bold flex items-center gap-2"><NotebookIcon className="h-6 w-6" /> Notebooks</h1>
                    <p className="text-muted-foreground">
                        Create and manage your analysis notebooks
                    </p>
                </div>

                {/* Create button */}
                <Dialog open={isCreateOpen} onOpenChange={setIsCreateOpen}>
                    <DialogTrigger asChild>
                        <Button>
                            <Plus className="h-4 w-4 mr-2" />
                            New Notebook
                        </Button>
                    </DialogTrigger>
                    <DialogContent className="sm:max-w-[500px]">
                        <DialogHeader>
                            <DialogTitle>Create New Notebook</DialogTitle>
                            <DialogDescription>
                                Configure your notebook settings
                            </DialogDescription>
                        </DialogHeader>

                        <div className="space-y-4 py-4">
                            {/* Name */}
                            <div className="space-y-2">
                                <label className="text-sm font-medium">Name</label>
                                <Input
                                    value={newName}
                                    onChange={(e) => setNewName(e.target.value)}
                                    placeholder="Notebook name"
                                />
                            </div>

                            {/* Language */}
                            <div className="space-y-2">
                                <label className="text-sm font-medium">Language</label>
                                <Select value={newLanguage} onValueChange={(v) => setNewLanguage(v as NotebookLanguage)}>
                                    <SelectTrigger>
                                        <SelectValue />
                                    </SelectTrigger>
                                    <SelectContent>
                                        <SelectItem value="python">Python</SelectItem>
                                        <SelectItem value="scala">Scala</SelectItem>
                                        <SelectItem value="sql">SQL</SelectItem>
                                    </SelectContent>
                                </Select>
                            </div>


                        </div>

                        <DialogFooter>
                            <Button variant="outline" onClick={() => setIsCreateOpen(false)}>
                                Cancel
                            </Button>
                            <Button onClick={handleCreate} disabled={isCreating || !newName.trim()}>
                                {isCreating && <Loader2 className="h-4 w-4 mr-2 animate-spin" />}
                                Create
                            </Button>
                        </DialogFooter>
                    </DialogContent>
                </Dialog>
            </div>

            {/* Loading */}
            {loading && (
                <div className="flex items-center justify-center h-64">
                    <Loader2 className="h-8 w-8 animate-spin" />
                </div>
            )}

            {/* Empty state */}
            {!loading && notebooks.length === 0 && (
                <Card className="border-dashed">
                    <CardContent className="flex flex-col items-center justify-center h-64">
                        <FileCode className="h-12 w-12 text-muted-foreground mb-4" />
                        <p className="text-muted-foreground mb-4">No notebooks yet</p>
                        <Button onClick={() => setIsCreateOpen(true)}>
                            <Plus className="h-4 w-4 mr-2" />
                            Create your first notebook
                        </Button>
                    </CardContent>
                </Card>
            )}

            {/* Notebook grid */}
            {!loading && notebooks.length > 0 && (
                <div className="grid grid-cols-1 md:grid-cols-2 lg:grid-cols-3 gap-4">
                    {notebooks.map((notebook) => (
                        <Card
                            key={notebook.id}
                            className="cursor-pointer hover:border-primary transition-colors"
                            onClick={() => handleOpen(notebook.id)}
                        >
                            <CardHeader className="pb-2">
                                <div className="flex items-start justify-between">
                                    <div className="flex-1 min-w-0">
                                        <CardTitle className="text-lg truncate">{notebook.name}</CardTitle>
                                        <CardDescription className="flex items-center gap-2 mt-1">
                                            <span className="capitalize">{notebook.language}</span>
                                            {notebook.is_public && (
                                                <span className="text-xs bg-blue-100 dark:bg-blue-900 text-blue-700 dark:text-blue-300 px-1.5 py-0.5 rounded">
                                                    Public
                                                </span>
                                            )}
                                        </CardDescription>
                                    </div>
                                    <DropdownMenu>
                                        <DropdownMenuTrigger asChild>
                                            <Button variant="ghost" size="icon" className="h-8 w-8" onClick={e => e.stopPropagation()}>
                                                <MoreVertical className="h-4 w-4" />
                                            </Button>
                                        </DropdownMenuTrigger>
                                        <DropdownMenuContent align="end">
                                            <DropdownMenuItem onClick={(e) => { e.stopPropagation(); handleRename(notebook.id, notebook.name); }}>
                                                <Pencil className="h-3.5 w-3.5 mr-2" /> Rename
                                            </DropdownMenuItem>
                                            <DropdownMenuItem className="text-destructive" onClick={(e) => { e.stopPropagation(); handleDelete(notebook.id, notebook.name); }}>
                                                <Trash2 className="h-3.5 w-3.5 mr-2" /> Delete
                                            </DropdownMenuItem>
                                        </DropdownMenuContent>
                                    </DropdownMenu>
                                </div>
                            </CardHeader>
                            <CardContent>
                                <div className="flex items-center gap-2 text-xs text-muted-foreground">
                                    <Clock className="h-3 w-3" />
                                    <span>
                                        Updated {new Date(notebook.updated_at).toLocaleDateString('en-US', { month: 'short', day: 'numeric', year: 'numeric' })}
                                    </span>
                                </div>
                            </CardContent>
                        </Card>
                    ))}
                </div>
            )}

            {/* Delete Confirmation */}
            <AlertDialog open={!!deleteTarget} onOpenChange={(open) => { if (!open) setDeleteTarget(null); }}>
                <AlertDialogContent>
                    <AlertDialogHeader>
                        <AlertDialogTitle>Delete "{deleteTarget?.name}"?</AlertDialogTitle>
                        <AlertDialogDescription>This will permanently delete the notebook and all its cells.</AlertDialogDescription>
                    </AlertDialogHeader>
                    <AlertDialogFooter>
                        <AlertDialogCancel>Cancel</AlertDialogCancel>
                        <AlertDialogAction className="bg-destructive text-destructive-foreground hover:bg-destructive/90" onClick={async () => { if (deleteTarget) await deleteNotebook(deleteTarget.id); setDeleteTarget(null); }}>Delete</AlertDialogAction>
                    </AlertDialogFooter>
                </AlertDialogContent>
            </AlertDialog>

            {/* Rename Dialog */}
            <Dialog open={!!renameTarget} onOpenChange={(open) => { if (!open) setRenameTarget(null); }}>
                <DialogContent className="max-w-sm">
                    <DialogHeader>
                        <DialogTitle>Rename Notebook</DialogTitle>
                    </DialogHeader>
                    <Input
                        value={renameValue}
                        onChange={(e) => setRenameValue(e.target.value)}
                        onKeyDown={(e) => { if (e.key === 'Enter') confirmRename(); }}
                        autoFocus
                    />
                    <DialogFooter>
                        <Button variant="outline" onClick={() => setRenameTarget(null)}>Cancel</Button>
                        <Button onClick={confirmRename} disabled={!renameValue.trim()}>Rename</Button>
                    </DialogFooter>
                </DialogContent>
            </Dialog>
        </div>
    );
}
