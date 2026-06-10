import React, { useState, useEffect, useCallback, useRef } from 'react';
import { Upload, FileText, Trash2, Download, FolderOpen, Copy, RefreshCw, ChevronLeft, FolderPlus, HardDrive, Globe, Lock } from 'lucide-react';
import { Dialog, DialogContent, DialogHeader, DialogTitle, DialogFooter } from '@/components/ui/dialog';
import { AlertDialog, AlertDialogAction, AlertDialogCancel, AlertDialogContent, AlertDialogDescription, AlertDialogFooter, AlertDialogHeader, AlertDialogTitle } from '@/components/ui/alert-dialog';
import { Input as DialogInput } from '@/components/ui/input';
import { toast } from 'sonner';
import { getUserDataPath, formatFileSize } from '@/services/notebookStorageService';
import * as minioService from '@/services/minioStorageService';
import { Button } from '@/components/ui/button';

// Scope is the virtual bucket name surfaced by the backend ("my" or "public").
// Sidebar never sees the real bucket or user prefix — backend hides them.
type Scope = 'my' | 'public';

interface ScopeMeta {
    name: Scope;
    display: string;
    can_write: boolean;
    s3a_root: string;   // full s3a:// URI for this scope, used for Copy Path
}

export const SidebarFiles: React.FC = () => {
    const [scopes, setScopes] = useState<ScopeMeta[]>([]);
    const [activeScope, setActiveScopeRaw] = useState<Scope>(() => {
        const saved = localStorage.getItem('sparklabx-files-scope');
        return saved === 'public' ? 'public' : 'my';
    });
    const setActiveScope = (s: Scope) => {
        setActiveScopeRaw(s);
        localStorage.setItem('sparklabx-files-scope', s);
        setCurrentPath('');
    };
    const [files, setFiles] = useState<minioService.MinioFile[]>([]);
    const [loading, setLoading] = useState(false);
    const [uploading, setUploading] = useState(false);
    const [uploadProgress, setUploadProgress] = useState(0);
    const [currentPath, setCurrentPathRaw] = useState<string>(() => {
        return localStorage.getItem('sparklabx-files-path') || '';
    });
    const setCurrentPath = (path: string) => {
        setCurrentPathRaw(path);
        localStorage.setItem('sparklabx-files-path', path);
    };
    const fileInputRef = useRef<HTMLInputElement>(null);

    const [createDialogOpen, setCreateDialogOpen] = useState(false);
    const [createDialogValue, setCreateDialogValue] = useState('');
    const [deleteTarget, setDeleteTarget] = useState<minioService.MinioFile | null>(null);

    const currentScope = scopes.find(s => s.name === activeScope);
    const canWrite = currentScope?.can_write ?? false;

    // Load scopes once (also fetches the s3a:// roots for Copy Path).
    useEffect(() => {
        (async () => {
            const { buckets } = await minioService.listBuckets();
            const pathInfo = await getUserDataPath();
            const privateRoot = (pathInfo as any)?.private_path || pathInfo?.path || '';
            const publicRoot = (pathInfo as any)?.public_path || '';
            const enriched: ScopeMeta[] = buckets.map(b => ({
                name: (b.name === 'public' ? 'public' : 'my') as Scope,
                display: b.display || (b.name === 'public' ? 'Public' : 'My Space'),
                can_write: b.can_write ?? (b.name !== 'public'),
                s3a_root: b.name === 'public' ? publicRoot : privateRoot,
            }));
            setScopes(enriched);
        })();
    }, []);

    const loadFiles = useCallback(async () => {
        setLoading(true);
        try {
            const fileList = await minioService.listObjects(activeScope, currentPath || undefined);
            setFiles(fileList);
        } catch (error) {
            console.error('Failed to load files:', error);
        } finally {
            setLoading(false);
        }
    }, [activeScope, currentPath]);

    useEffect(() => {
        loadFiles();
    }, [loadFiles]);

    const handleUpload = async (event: React.ChangeEvent<HTMLInputElement>) => {
        const uploadedFiles = event.target.files;
        if (!uploadedFiles || uploadedFiles.length === 0) return;
        setUploading(true);
        setUploadProgress(0);
        try {
            for (let i = 0; i < uploadedFiles.length; i++) {
                const file = uploadedFiles[i];
                const ok = await minioService.uploadObject(
                    activeScope, file, currentPath || undefined,
                    (percent) => setUploadProgress(percent)
                );
                if (ok) toast.success(`Uploaded ${file.name}`);
                else toast.error(`Failed to upload ${file.name}`);
            }
            loadFiles();
        } finally {
            setUploading(false);
            setUploadProgress(0);
            if (fileInputRef.current) fileInputRef.current.value = '';
        }
    };

    const handleCreateFolder = () => {
        if (!canWrite) return;
        setCreateDialogValue('');
        setCreateDialogOpen(true);
    };

    const handleCreateFolderSubmit = async () => {
        const name = createDialogValue.trim();
        if (!name) return;
        const folderKey = (currentPath || '') + name + '/';
        try {
            const ok = await minioService.createFolder(activeScope, folderKey);
            if (ok) { toast.success(`Created folder "${name}"`); loadFiles(); }
            else toast.error('Failed to create folder');
        } catch {
            toast.error('Failed to create folder');
        }
        setCreateDialogOpen(false);
        setCreateDialogValue('');
    };

    const confirmDelete = async () => {
        if (!deleteTarget) return;
        const ok = await minioService.deleteObject(activeScope, deleteTarget.key);
        if (ok) { toast.success('Deleted'); loadFiles(); }
        else toast.error('Failed to delete');
        setDeleteTarget(null);
    };

    const copyPath = (path: string) => {
        navigator.clipboard.writeText(path);
        toast.success('Path copied');
    };

    // Build full s3a:// URI for a key in the current scope. Falls back to "/key"
    // if the scope root hasn't loaded yet — defaultFS will still resolve it.
    const buildFullPath = (key: string) => {
        const root = currentScope?.s3a_root || '';
        return root ? `${root}${key}` : `/${key}`;
    };

    const navigateToFolder = (folder: minioService.MinioFile) => setCurrentPath(folder.key);
    const navigateUp = () => {
        const parts = currentPath.split('/').filter(Boolean);
        parts.pop();
        setCurrentPath(parts.length > 0 ? parts.join('/') + '/' : '');
    };

    return (
        <div className="flex flex-col h-full bg-background text-foreground text-xs">
            {/* Scope tabs */}
            <div className="flex border-b border-border">
                {scopes.map(s => {
                    const Icon = s.name === 'public' ? Globe : HardDrive;
                    const active = activeScope === s.name;
                    return (
                        <button
                            key={s.name}
                            onClick={() => setActiveScope(s.name)}
                            className={`flex-1 flex items-center justify-center gap-1.5 px-2 py-2 text-[11px] font-medium border-b-2 transition-colors ${active ? 'border-primary text-primary bg-primary/5' : 'border-transparent text-muted-foreground hover:text-foreground hover:bg-muted/50'}`}
                            title={s.display}
                        >
                            <Icon className="size-3.5" />
                            <span>{s.display}</span>
                            {!s.can_write && <Lock className="size-2.5 opacity-60" />}
                        </button>
                    );
                })}
            </div>

            {/* Action bar */}
            <div className="flex items-center gap-1 p-2 border-b border-border">
                <input
                    ref={fileInputRef}
                    type="file"
                    multiple
                    onChange={handleUpload}
                    className="hidden"
                    id="sidebar-file-upload"
                    disabled={!canWrite}
                />
                <label
                    htmlFor={canWrite ? "sidebar-file-upload" : undefined}
                    className={`flex-1 flex items-center justify-center gap-1.5 px-2 py-1 rounded h-7 font-medium transition-colors ${canWrite ? 'bg-primary text-primary-foreground cursor-pointer hover:bg-primary/90' : 'bg-muted text-muted-foreground cursor-not-allowed opacity-60'}`}
                    title={canWrite ? 'Upload file' : 'Read-only space'}
                >
                    <Upload className="size-3.5" />
                    <span>Upload</span>
                </label>

                <Button
                    variant="ghost"
                    size="icon"
                    className={`h-7 w-7 ${!canWrite ? 'opacity-50 cursor-not-allowed' : ''}`}
                    onClick={canWrite ? handleCreateFolder : undefined}
                    disabled={loading || !canWrite}
                    title={canWrite ? 'New folder' : 'Read-only space'}
                >
                    <FolderPlus className="size-3.5" />
                </Button>

                <Button
                    variant="ghost"
                    size="icon"
                    className="h-7 w-7"
                    onClick={loadFiles}
                    disabled={loading}
                    title="Refresh"
                >
                    <RefreshCw className={`size-3.5 ${loading ? 'animate-spin' : ''}`} />
                </Button>
            </div>

            {/* Breadcrumb — fixed row height so the panel doesn't grow
                when navigating into a folder (the back arrow only renders
                in subfolders; without a reserved slot the row would
                stretch when it appears). */}
            <div className="px-2 py-1.5 bg-muted/30 border-b border-border">
                <div className="flex items-center gap-1 text-xs overflow-hidden text-nowrap h-5">
                    {currentPath ? (
                        <Button variant="ghost" size="icon" className="h-5 w-5 shrink-0 -ml-1" onClick={navigateUp} title="Go Up">
                            <ChevronLeft className="size-3.5" />
                        </Button>
                    ) : (
                        <span className="w-5 shrink-0 -ml-1" aria-hidden />
                    )}
                    <span className="truncate font-mono text-[10px] flex-1" title={'/' + currentPath}>
                        /{currentPath}
                    </span>
                    <button
                        onClick={() => copyPath(buildFullPath(currentPath))}
                        className="p-0.5 hover:bg-muted rounded text-muted-foreground hover:text-foreground shrink-0"
                        title="Copy path"
                    >
                        <Copy className="size-3" />
                    </button>
                </div>
            </div>

            {/* Upload progress */}
            {uploading && (
                <div className="px-2 py-1 border-b border-border bg-muted/10">
                    <div className="flex items-center gap-2">
                        <div className="flex-1 bg-muted rounded-full h-1.5">
                            <div className="bg-primary h-1.5 rounded-full transition-all" style={{ width: `${uploadProgress}%` }} />
                        </div>
                        <span className="text-[10px] text-muted-foreground">{uploadProgress}%</span>
                    </div>
                </div>
            )}

            {/* File list */}
            <div className="flex-1 overflow-y-auto">
                <div className="p-1 space-y-0.5">
                    {files.length === 0 && !loading && (
                        <div className="text-center py-8 text-muted-foreground">
                            <p className="text-[10px]">No files found</p>
                        </div>
                    )}
                    {files.map((file) => (
                        <div key={file.key} className="group flex items-center gap-2 p-1.5 rounded hover:bg-muted/50 transition-colors h-7">
                            {file.is_folder ? (
                                <FolderOpen className="size-3.5 text-yellow-500 shrink-0" />
                            ) : (
                                <FileText className="size-3.5 text-blue-500 shrink-0" />
                            )}
                            {/* Single-line row so swapping a folder ↔ file at
                                the same position doesn't change the row
                                height. File size moves inline to the right
                                instead of stacking under the name. */}
                            <div className="flex-1 min-w-0 overflow-hidden flex items-baseline gap-2">
                                {file.is_folder ? (
                                    <span onClick={() => navigateToFolder(file)} className="font-medium cursor-pointer truncate hover:text-primary transition-colors text-[11px] flex-1">
                                        {file.name}
                                    </span>
                                ) : (
                                    <span className="truncate text-[11px] flex-1" title={file.name}>{file.name}</span>
                                )}
                                {!file.is_folder && (
                                    <span className="text-[9px] text-muted-foreground shrink-0">{formatFileSize(file.size)}</span>
                                )}
                            </div>
                            <div className="flex opacity-0 group-hover:opacity-100 transition-opacity gap-0.5">
                                <button
                                    onClick={(e) => { e.stopPropagation(); copyPath(buildFullPath(file.key)); }}
                                    className="p-1 hover:bg-muted rounded text-muted-foreground hover:text-foreground"
                                    title="Copy Path"
                                >
                                    <Copy className="size-3" />
                                </button>
                                {!file.is_folder && (
                                    <>
                                        <button
                                            onClick={async () => {
                                                try {
                                                    const url = minioService.getDownloadUrl(activeScope, file.key);
                                                    const resp = await import('@/config/axios').then(m => m.default.get(url, { responseType: 'blob' }));
                                                    const blobUrl = URL.createObjectURL(resp.data);
                                                    const a = document.createElement('a');
                                                    a.href = blobUrl;
                                                    a.download = file.name;
                                                    a.click();
                                                    URL.revokeObjectURL(blobUrl);
                                                } catch { toast.error('Download failed'); }
                                            }}
                                            className="p-1 hover:bg-muted rounded text-muted-foreground hover:text-foreground"
                                            title="Download"
                                        >
                                            <Download className="size-3" />
                                        </button>
                                        {canWrite && (
                                            <button
                                                onClick={() => setDeleteTarget(file)}
                                                className="p-1 hover:bg-destructive/10 rounded text-muted-foreground hover:text-destructive"
                                                title="Delete"
                                            >
                                                <Trash2 className="size-3" />
                                            </button>
                                        )}
                                    </>
                                )}
                            </div>
                        </div>
                    ))}
                </div>
            </div>

            {/* Delete confirm */}
            <AlertDialog open={!!deleteTarget} onOpenChange={(open) => { if (!open) setDeleteTarget(null); }}>
                <AlertDialogContent>
                    <AlertDialogHeader>
                        <AlertDialogTitle>Delete "{deleteTarget?.name}"?</AlertDialogTitle>
                        <AlertDialogDescription>This action cannot be undone.</AlertDialogDescription>
                    </AlertDialogHeader>
                    <AlertDialogFooter>
                        <AlertDialogCancel>Cancel</AlertDialogCancel>
                        <AlertDialogAction className="bg-destructive text-destructive-foreground hover:bg-destructive/90" onClick={confirmDelete}>Delete</AlertDialogAction>
                    </AlertDialogFooter>
                </AlertDialogContent>
            </AlertDialog>

            {/* Create folder dialog */}
            <Dialog open={createDialogOpen} onOpenChange={setCreateDialogOpen}>
                <DialogContent className="max-w-xs">
                    <DialogHeader>
                        <DialogTitle>Create Folder</DialogTitle>
                    </DialogHeader>
                    <DialogInput
                        placeholder="Folder name"
                        value={createDialogValue}
                        onChange={e => setCreateDialogValue(e.target.value)}
                        onKeyDown={e => { if (e.key === 'Enter') handleCreateFolderSubmit(); }}
                        autoFocus
                    />
                    <DialogFooter>
                        <Button variant="outline" size="sm" onClick={() => setCreateDialogOpen(false)}>Cancel</Button>
                        <Button size="sm" onClick={handleCreateFolderSubmit} disabled={!createDialogValue.trim()}>Create</Button>
                    </DialogFooter>
                </DialogContent>
            </Dialog>
        </div>
    );
};
