import React, { useState, useEffect, useCallback, useRef } from 'react';
import { Card, CardContent, CardHeader, CardTitle } from '../ui/card';
import { Button } from '../ui/button';
import { Input } from '../ui/input';
import { Dialog, DialogContent, DialogHeader, DialogTitle, DialogFooter } from '../ui/dialog';
import { ConfirmDeleteDialog } from '../ui/confirm-delete-dialog';
import {
  Upload, FileText, Trash2, Download, FolderOpen, Copy, RefreshCw,
  ChevronLeft, Database, FolderPlus, Plus, HardDrive, Search, Eye,
} from 'lucide-react';
import { toast } from 'sonner';
import axios from 'axios';
import * as minioService from '../../services/minioStorageService';
import { parseCsv, parseJsonTable, formatSize } from '../../lib/dataUtils';

const StoragePage: React.FC = () => {
  const [createBucketOpen, setCreateBucketOpen] = useState(false);
  const [newBucketName, setNewBucketName] = useState('');
  const [buckets, setBuckets] = useState<minioService.MinioBucket[]>([]);
  const [selectedBucket, setSelectedBucketRaw] = useState(() => localStorage.getItem('sparklabx-storage-bucket') || '');
  const setSelectedBucket = (b: string) => { setSelectedBucketRaw(b); localStorage.setItem('sparklabx-storage-bucket', b); };
  const [files, setFiles] = useState<minioService.MinioFile[]>([]);
  const [loading, setLoading] = useState(false);
  const [uploading, setUploading] = useState(false);
  const [uploadProgress, setUploadProgress] = useState(0);
  const [currentPath, setCurrentPathRaw] = useState(() => localStorage.getItem('sparklabx-storage-path') || '');
  const setCurrentPath = (p: string) => { setCurrentPathRaw(p); localStorage.setItem('sparklabx-storage-path', p); };
  const [searchQuery, setSearchQuery] = useState('');
  const [available, setAvailable] = useState(false);
  const fileInputRef = useRef<HTMLInputElement>(null);
  const [confirmDelete, setConfirmDelete] = useState<{ type: 'bucket' | 'file'; name: string; action: () => Promise<void> } | null>(null);
  const [previewFile, setPreviewFile] = useState<minioService.MinioFile | null>(null);

  // Load buckets
  const loadBuckets = useCallback(async () => {
    const result = await minioService.listBuckets();
    setBuckets(result.buckets);
    setAvailable(result.available);
    // Clear stale selection from localStorage if the bucket no longer exists
    if (selectedBucket && !result.buckets.some(b => b.name === selectedBucket)) {
      setSelectedBucket('');
      setCurrentPath('');
      return;
    }
    if (!selectedBucket && result.buckets.length > 0) {
      setSelectedBucket(result.buckets[0].name);
    }
  }, [selectedBucket]);

  // Load files
  const loadFiles = useCallback(async () => {
    if (!selectedBucket) { setFiles([]); return; }
    setLoading(true);
    const fileList = await minioService.listObjects(selectedBucket, currentPath);
    setFiles(fileList);
    setLoading(false);
  }, [selectedBucket, currentPath]);

  useEffect(() => { loadBuckets(); }, [loadBuckets]);
  useEffect(() => { loadFiles(); }, [loadFiles]);

  // Create bucket
  const handleCreateBucket = async () => {
    if (!newBucketName.trim()) return;
    const ok = await minioService.createBucket(newBucketName.trim());
    if (ok) {
      toast.success(`Bucket "${newBucketName}" created`);
      setSelectedBucket(newBucketName.trim());
      loadBuckets();
    } else {
      toast.error('Failed to create bucket');
    }
    setCreateBucketOpen(false);
    setNewBucketName('');
  };

  // Create folder
  const [createFolderOpen, setCreateFolderOpen] = useState(false);
  const [newFolderName, setNewFolderName] = useState('');
  const handleCreateFolder = async () => {
    if (!selectedBucket || !newFolderName.trim()) return;
    const ok = await minioService.createFolder(selectedBucket, currentPath + newFolderName.trim() + '/');
    if (ok) {
      toast.success(`Folder "${newFolderName}" created`);
      loadFiles();
    } else {
      toast.error('Failed to create folder');
    }
    setCreateFolderOpen(false);
    setNewFolderName('');
  };

  // Upload files
  const handleUpload = async (e: React.ChangeEvent<HTMLInputElement>) => {
    if (!e.target.files || !selectedBucket) return;
    setUploading(true);
    setUploadProgress(0);
    const filesArr = Array.from(e.target.files);

    for (let i = 0; i < filesArr.length; i++) {
      const ok = await minioService.uploadObject(
        selectedBucket, filesArr[i], currentPath,
        pct => setUploadProgress(Math.round(((i + pct / 100) / filesArr.length) * 100))
      );
      if (ok) toast.success(`Uploaded ${filesArr[i].name}`);
      else toast.error(`Failed: ${filesArr[i].name}`);
    }

    setUploading(false);
    setUploadProgress(0);
    if (fileInputRef.current) fileInputRef.current.value = '';
    loadFiles();
  };

  // Delete
  const handleDelete = (file: minioService.MinioFile) => {
    setConfirmDelete({
      type: 'file',
      name: file.name,
      action: async () => {
        const ok = await minioService.deleteObject(selectedBucket, file.key);
        if (ok) { toast.success('Deleted'); loadFiles(); }
        else toast.error('Failed to delete');
      },
    });
  };

  // Navigate
  const navigateFolder = (folder: minioService.MinioFile) => setCurrentPath(folder.key);
  const navigateUp = () => {
    const parts = currentPath.split('/').filter(Boolean);
    parts.pop();
    setCurrentPath(parts.length > 0 ? parts.join('/') + '/' : '');
  };

  // Copy path
  const copyPath = (path: string) => {
    navigator.clipboard.writeText(path);
    toast.success('Copied');
  };

  // Filtered files
  const filteredFiles = searchQuery
    ? files.filter(f => f.name.toLowerCase().includes(searchQuery.toLowerCase()))
    : files;

  if (!available) {
    return (
      <div className="space-y-6">
        <h1 className="text-2xl font-bold flex items-center gap-2"><Database className="h-6 w-6" /> Storage</h1>
        <Card>
          <CardContent className="py-12 text-center text-muted-foreground">
            <Database className="h-12 w-12 mx-auto mb-3 opacity-30" />
            <p>MinIO is not configured.</p>
            <p className="text-xs mt-1">Set MINIO_ENDPOINT, MINIO_ACCESS_KEY, MINIO_SECRET_KEY in environment.</p>
          </CardContent>
        </Card>
      </div>
    );
  }

  return (
    <div className="space-y-4">
      <div className="flex items-center justify-between">
        <h1 className="text-2xl font-bold flex items-center gap-2"><Database className="h-6 w-6" /> Storage</h1>
        <Button variant="outline" onClick={() => setCreateBucketOpen(true)}>
          <Plus className="h-4 w-4 mr-1" /> New Bucket
        </Button>
      </div>

      <div className="grid grid-cols-[250px_1fr] gap-4">
        {/* Bucket list */}
        <Card>
          <CardHeader className="py-3 px-4">
            <CardTitle className="text-sm">Buckets</CardTitle>
          </CardHeader>
          <CardContent className="p-0">
            {buckets.map(b => (
              <div
                key={b.name}
                className={`w-full flex items-center gap-2 px-4 py-2 text-sm hover:bg-muted/50 border-b last:border-b-0 group ${selectedBucket === b.name ? 'bg-primary/10 text-primary font-medium' : ''
                  }`}
              >
                <button
                  onClick={() => { setSelectedBucket(b.name); setCurrentPath(''); }}
                  className="flex items-center gap-2 flex-1 min-w-0 text-left"
                >
                  <HardDrive className="h-3.5 w-3.5 shrink-0" />
                  <span className="truncate">{b.name}</span>
                </button>
                <Button size="sm" variant="ghost" className="h-6 w-6 p-0 opacity-0 group-hover:opacity-100 text-destructive"
                  onClick={(e) => {
                    e.stopPropagation();
                    setConfirmDelete({
                      type: 'bucket',
                      name: b.name,
                      action: async () => {
                        const ok = await minioService.deleteBucket(b.name);
                        if (ok) {
                          toast.success(`Bucket "${b.name}" deleted`);
                          if (selectedBucket === b.name) setSelectedBucket('');
                          loadBuckets();
                        } else {
                          toast.error('Failed to delete bucket (must be empty)');
                        }
                      },
                    });
                  }}>
                  <Trash2 className="h-3 w-3" />
                </Button>
              </div>
            ))}
            {buckets.length === 0 && (
              <div className="px-4 py-6 text-xs text-muted-foreground text-center">No buckets</div>
            )}
          </CardContent>
        </Card>

        {/* File browser */}
        <Card>
          <CardHeader className="py-3 px-4 flex flex-row items-center justify-between">
            <div className="flex items-center gap-2 flex-1 min-w-0">
              {currentPath && (
                <Button size="sm" variant="ghost" className="h-7 px-2" onClick={navigateUp}>
                  <ChevronLeft className="h-4 w-4" />
                </Button>
              )}
              <span className="text-sm font-mono text-muted-foreground truncate">
                {selectedBucket ? `${selectedBucket}/${currentPath}` : 'Select a bucket'}
              </span>
            </div>
            <div className="flex gap-1">
              <input ref={fileInputRef} type="file" multiple onChange={handleUpload} className="hidden" id="storage-upload" disabled={!selectedBucket} />
              <label htmlFor="storage-upload"
                className={`inline-flex items-center gap-1 px-3 py-1.5 text-xs font-medium rounded-md bg-primary text-primary-foreground ${!selectedBucket ? 'opacity-50 cursor-not-allowed pointer-events-none' : 'cursor-pointer hover:bg-primary/90'}`}>
                <Upload className="h-3.5 w-3.5" /> Upload
              </label>
              <Button size="sm" variant="outline" className="h-8" onClick={() => setCreateFolderOpen(true)} disabled={!selectedBucket}>
                <FolderPlus className="h-3.5 w-3.5 mr-1" /> Folder
              </Button>
              <Button size="sm" variant="ghost" className="h-8 w-8 p-0" onClick={loadFiles}>
                <RefreshCw className={`h-3.5 w-3.5 ${loading ? 'animate-spin' : ''}`} />
              </Button>
            </div>
          </CardHeader>
          <CardContent className="p-0">
            {/* Upload progress */}
            {uploading && (
              <div className="px-4 py-2 border-b">
                <div className="flex items-center gap-2">
                  <div className="flex-1 bg-muted rounded-full h-1.5">
                    <div className="bg-primary h-1.5 rounded-full transition-all" style={{ width: `${uploadProgress}%` }} />
                  </div>
                  <span className="text-xs text-muted-foreground">{uploadProgress}%</span>
                </div>
              </div>
            )}

            {/* Search */}
            {files.length > 5 && (
              <div className="px-4 py-2 border-b">
                <div className="flex items-center gap-2">
                  <Search className="h-3.5 w-3.5 text-muted-foreground" />
                  <Input placeholder="Filter files..." value={searchQuery}
                    onChange={e => setSearchQuery(e.target.value)} className="h-7 text-xs border-0 shadow-none focus-visible:ring-0" />
                </div>
              </div>
            )}

            {/* File table */}
            {!selectedBucket ? (
              <div className="px-4 py-12 text-center text-muted-foreground text-sm">Select a bucket to browse</div>
            ) : loading ? (
              <div className="px-4 py-12 text-center text-muted-foreground text-sm">Loading...</div>
            ) : filteredFiles.length === 0 ? (
              <div className="px-4 py-12 text-center text-muted-foreground text-sm">Empty</div>
            ) : (
              <table className="w-full text-sm">
                <thead>
                  <tr className="border-b text-xs text-muted-foreground">
                    <th className="text-left px-4 py-2 font-medium">Name</th>
                    <th className="text-right px-4 py-2 font-medium w-24">Size</th>
                    <th className="text-right px-4 py-2 font-medium w-44">Modified</th>
                    <th className="text-right px-4 py-2 font-medium w-28">Actions</th>
                  </tr>
                </thead>
                <tbody>
                  {filteredFiles.map(f => (
                    <tr key={f.key} className="border-b hover:bg-muted/30 group">
                      <td className="px-4 py-2">
                        <div className="flex items-center gap-2 cursor-pointer"
                          onClick={() => f.is_folder ? navigateFolder(f) : undefined}>
                          {f.is_folder ? (
                            <FolderOpen className="h-4 w-4 text-yellow-500 shrink-0" />
                          ) : (
                            <FileText className="h-4 w-4 text-blue-500 shrink-0" />
                          )}
                          <span className={f.is_folder ? 'font-medium hover:text-primary' : ''}>{f.name}</span>
                        </div>
                      </td>
                      <td className="px-4 py-2 text-right text-xs text-muted-foreground">
                        {f.is_folder ? '-' : minioService.formatFileSize(f.size)}
                      </td>
                      <td className="px-4 py-2 text-right text-xs text-muted-foreground">
                        {f.last_modified ? new Date(f.last_modified).toLocaleString() : '-'}
                      </td>
                      <td className="px-4 py-2 text-right">
                        <div className="flex justify-end gap-1 opacity-0 group-hover:opacity-100 transition-opacity">
                          <Button size="sm" variant="ghost" className="h-7 w-7 p-0"
                            onClick={() => copyPath(`s3a://${selectedBucket}/${f.key}`)} title="Copy S3 path">
                            <Copy className="h-3.5 w-3.5" />
                          </Button>
                          {!f.is_folder && /\.(csv|tsv|json|jsonl|parquet)$/i.test(f.name) && (
                            <Button size="sm" variant="ghost" className="h-7 w-7 p-0"
                              onClick={() => setPreviewFile(f)} title="Preview">
                              <Eye className="h-3.5 w-3.5" />
                            </Button>
                          )}
                          {!f.is_folder && (
                            <Button size="sm" variant="ghost" className="h-7 w-7 p-0" title="Download"
                              onClick={async () => {
                                try {
                                  const resp = await axios.get(minioService.getDownloadUrl(selectedBucket, f.key), { responseType: 'blob' });
                                  const url = URL.createObjectURL(resp.data);
                                  const a = document.createElement('a');
                                  a.href = url;
                                  a.download = f.name;
                                  a.click();
                                  URL.revokeObjectURL(url);
                                } catch { toast.error('Download failed'); }
                              }}>
                              <Download className="h-3.5 w-3.5" />
                            </Button>
                          )}
                          <Button size="sm" variant="ghost" className="h-7 w-7 p-0 text-destructive"
                            onClick={() => handleDelete(f)} title="Delete">
                            <Trash2 className="h-3.5 w-3.5" />
                          </Button>
                        </div>
                      </td>
                    </tr>
                  ))}
                </tbody>
              </table>
            )}
          </CardContent>
        </Card>
      </div>

      {/* Create Bucket Dialog */}
      <Dialog open={createBucketOpen} onOpenChange={setCreateBucketOpen}>
        <DialogContent className="max-w-sm">
          <DialogHeader>
            <DialogTitle>Create Bucket</DialogTitle>
          </DialogHeader>
          <Input placeholder="Bucket name" value={newBucketName}
            onChange={e => setNewBucketName(e.target.value)}
            onKeyDown={e => { if (e.key === 'Enter') handleCreateBucket(); }}
            autoFocus />
          <DialogFooter>
            <Button variant="outline" onClick={() => { setCreateBucketOpen(false); setNewBucketName(''); }}>Cancel</Button>
            <Button onClick={handleCreateBucket} disabled={!newBucketName.trim()}>Create</Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>

      {/* Create Folder Dialog */}
      <Dialog open={createFolderOpen} onOpenChange={setCreateFolderOpen}>
        <DialogContent className="max-w-sm">
          <DialogHeader>
            <DialogTitle>Create Folder</DialogTitle>
          </DialogHeader>
          <Input placeholder="Folder name" value={newFolderName}
            onChange={e => setNewFolderName(e.target.value)}
            onKeyDown={e => { if (e.key === 'Enter') handleCreateFolder(); }}
            autoFocus />
          <DialogFooter>
            <Button variant="outline" onClick={() => { setCreateFolderOpen(false); setNewFolderName(''); }}>Cancel</Button>
            <Button onClick={handleCreateFolder} disabled={!newFolderName.trim()}>Create</Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>

      {/* Confirm Delete Dialog */}
      {confirmDelete && (
        <ConfirmDeleteDialog
          open={!!confirmDelete}
          onOpenChange={() => setConfirmDelete(null)}
          title={`Delete ${confirmDelete.type === 'bucket' ? 'bucket' : 'file'}?`}
          description={confirmDelete.type === 'bucket'
            ? `Delete bucket "${confirmDelete.name}"? Bucket must be empty.`
            : `Delete "${confirmDelete.name}"? This action cannot be undone.`}
          confirmText={confirmDelete.name}
          onConfirm={async () => { await confirmDelete.action(); }}
        />
      )}

      {/* File Preview Dialog */}
      <Dialog open={!!previewFile} onOpenChange={(open) => !open && setPreviewFile(null)}>
        {previewFile && (
          <StorageFilePreview bucket={selectedBucket} file={previewFile} onClose={() => setPreviewFile(null)} />
        )}
      </Dialog>
    </div>
  );
};

const StorageFilePreview: React.FC<{ bucket: string; file: minioService.MinioFile; onClose: () => void }> = ({ bucket, file, onClose }) => {
  const [previewData, setPreviewData] = useState<{ headers: string[]; rows: string[][] } | null>(null);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState('');

  const ext = file.name.split('.').pop()?.toLowerCase() || '';

  useEffect(() => {
    (async () => {
      setLoading(true);
      setError('');
      try {
        const baseUrl = minioService.getDownloadUrl(bucket, file.key);

        if (ext === 'parquet') {
          const resp = await axios.get(baseUrl, { responseType: 'arraybuffer' });
          const { parquetMetadata, parquetRead } = await import('hyparquet');
          const arrayBuffer = resp.data as ArrayBuffer;
          const metadata = parquetMetadata(arrayBuffer);
          const headers = metadata.schema.slice(1).map((s: any) => s.name || `col_${s}`);
          await parquetRead({
            file: arrayBuffer,
            onComplete: (rows: any[]) => {
              const parsed = rows.map((r: any) =>
                Array.isArray(r)
                  ? r.map(v => String(v ?? ''))
                  : headers.map((h: string) => String(r[h] ?? ''))
              );
              setPreviewData({ headers, rows: parsed });
            },
          });
        } else {
          // fetch first 1MB only for preview
          const previewUrl = `${baseUrl}&preview=true`;
          const resp = await axios.get(previewUrl, { responseType: 'text' });
          const text = resp.data;

          if (ext === 'csv' || ext === 'tsv') {
            setPreviewData(parseCsv(text));
          } else if (ext === 'json' || ext === 'jsonl') {
            if (ext === 'jsonl') {
              const lines = text.trim().split('\n').filter((l: string) => l.trim());
              const parsed = parseJsonTable('[' + lines.join(',') + ']');
              setPreviewData(parsed);
              if (!parsed) setError('Could not parse as table');
            } else {
              const parsed = parseJsonTable(text);
              setPreviewData(parsed);
              if (!parsed) setError('Could not parse as table');
            }
          } else {
            setError(`Preview not supported for .${ext} files`);
          }
        }
      } catch (err: any) {
        setError(err.response?.data?.error || 'Failed to load file');
      } finally {
        setLoading(false);
      }
    })();
  }, [bucket, file, ext]);

  const maxRows = 100;

  return (
    <DialogContent className="max-w-4xl max-h-[80vh] flex flex-col">
      <DialogHeader>
        <DialogTitle className="flex items-center gap-2">
          <FileText className="h-4 w-4" />
          {file.name}
          {previewData && <span className="text-xs text-muted-foreground font-normal">({previewData.rows.length} rows{previewData.rows.length > maxRows ? `, showing first ${maxRows}` : ''})</span>}
        </DialogTitle>
      </DialogHeader>
      <div className="flex-1 overflow-auto min-h-0">
        {loading ? (
          <div className="py-8 text-center text-muted-foreground">Loading preview...</div>
        ) : error ? (
          <div className="py-8 text-center text-muted-foreground">{error}</div>
        ) : previewData ? (
          <table className="w-full text-xs border-collapse">
            <thead className="sticky top-0 bg-secondary z-10">
              <tr>
                <th className="border px-2 py-1.5 text-left text-muted-foreground font-medium">#</th>
                {previewData.headers.map((h, i) => (
                  <th key={i} className="border px-2 py-1.5 text-left font-medium whitespace-nowrap">{h}</th>
                ))}
              </tr>
            </thead>
            <tbody>
              {previewData.rows.slice(0, maxRows).map((row, i) => (
                <tr key={i} className="hover:bg-accent/50">
                  <td className="border px-2 py-1 text-muted-foreground">{i + 1}</td>
                  {row.map((cell, j) => (
                    <td key={j} className="border px-2 py-1 max-w-[200px] truncate" title={cell}>{cell}</td>
                  ))}
                </tr>
              ))}
            </tbody>
          </table>
        ) : null}
      </div>
    </DialogContent>
  );
};

export default StoragePage;
