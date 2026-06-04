/**
 * OBJECT STORAGE Service
 * Browse MinIO buckets and objects from admin notebook
 */
import api from '../config/axios';

const BASE_URL = '/api/v1/minio';

export interface MinioBucket {
    name: string;
    display?: string;   // human label (e.g. "My Space", "Public") for scope-based listing
    can_write?: boolean;
    creation_date?: string;
}

export interface MinioFile {
    key: string;
    name: string;
    size: number;
    last_modified?: string;
    is_folder: boolean;
}

export async function listBuckets(): Promise<{ buckets: MinioBucket[]; available: boolean }> {
    try {
        const response = await api.get(`${BASE_URL}/buckets`);
        return response.data;
    } catch {
        return { buckets: [], available: false };
    }
}

export async function createBucket(name: string): Promise<boolean> {
    try {
        await api.put(`${BASE_URL}/buckets/${encodeURIComponent(name)}`);
        return true;
    } catch {
        return false;
    }
}

export async function listObjects(bucket: string, prefix?: string): Promise<MinioFile[]> {
    try {
        const response = await api.get(`${BASE_URL}/buckets/${encodeURIComponent(bucket)}/objects`, {
            params: prefix ? { prefix } : undefined,
        });
        return response.data.files || [];
    } catch {
        return [];
    }
}

export async function uploadObject(
    bucket: string,
    file: File,
    path?: string,
    onProgress?: (percent: number) => void,
): Promise<boolean> {
    try {
        const formData = new FormData();
        formData.append('file', file);
        await api.post(`${BASE_URL}/buckets/${encodeURIComponent(bucket)}/upload`, formData, {
            params: path ? { path } : undefined,
            headers: { 'Content-Type': 'multipart/form-data' },
            onUploadProgress: (e) => {
                if (onProgress && e.total) onProgress(Math.round((e.loaded * 100) / e.total));
            },
        });
        return true;
    } catch {
        return false;
    }
}

export function getDownloadUrl(bucket: string, key: string): string {
    return `${BASE_URL}/buckets/${encodeURIComponent(bucket)}/download?key=${encodeURIComponent(key)}`;
}

export async function deleteBucket(bucket: string): Promise<boolean> {
    try {
        await api.delete(`${BASE_URL}/buckets/${encodeURIComponent(bucket)}`);
        return true;
    } catch {
        return false;
    }
}

export async function createFolder(bucket: string, folderKey: string): Promise<boolean> {
    try {
        await api.post(`${BASE_URL}/buckets/${encodeURIComponent(bucket)}/folder`, { key: folderKey });
        return true;
    } catch {
        return false;
    }
}

export async function deleteObject(bucket: string, key: string): Promise<boolean> {
    try {
        await api.delete(`${BASE_URL}/buckets/${encodeURIComponent(bucket)}/objects`, {
            params: { key },
        });
        return true;
    } catch {
        return false;
    }
}

export function formatFileSize(bytes: number): string {
    if (bytes === 0) return '0 B';
    const k = 1024;
    const sizes = ['B', 'KB', 'MB', 'GB', 'TB'];
    const i = Math.floor(Math.log(bytes) / Math.log(k));
    return parseFloat((bytes / Math.pow(k, i)).toFixed(2)) + ' ' + sizes[i];
}
