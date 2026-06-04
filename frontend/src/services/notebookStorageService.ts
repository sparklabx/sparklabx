/**
 * Notebook Storage Service
 * Handles file upload/list/delete for user's notebook data folder
 */
import api from '../config/axios';

// Types
export interface UserFileInfo {
    key: string;
    name: string;
    size: number;
    last_modified?: string;
    is_folder: boolean;
}

export interface ListFilesResponse {
    files: UserFileInfo[];
    prefix: string;
    path: string;
}

export interface UploadFileResponse {
    success: boolean;
    key: string;
    name: string;
    size: number;
    path: string; // S3A path for use in Spark
}

export interface UserDataPathResponse {
    path: string;             // legacy alias for private_path
    private_path?: string;    // s3a://workspace/users/<username>/
    public_path?: string;     // s3a://workspace/public/
    bucket: string;
    prefix: string;
    region?: string;
    endpoint?: string;        // Set when using MinIO/custom S3 endpoint (K8s on-prem)
    available?: boolean;
}

const BASE_URL = '/api/v1/notebooks/storage';

/**
 * List files in user's data folder
 */
export async function listUserFiles(path?: string, scope?: 'student' | 'dataset'): Promise<UserFileInfo[]> {
    try {
        const params: any = {};
        if (path) params.path = path;
        if (scope) params.scope = scope;
        const response = await api.get<ListFilesResponse>(`${BASE_URL}/files`, { params });
        return response.data.files || [];
    } catch (error) {
        console.error('Failed to list user files:', error);
        return [];
    }
}

/**
 * Upload a file to user's data folder
 */
export async function uploadUserFile(
    file: File,
    path?: string,
    onProgress?: (percent: number) => void
): Promise<UploadFileResponse | null> {
    try {
        const formData = new FormData();
        formData.append('file', file);

        const response = await api.post<UploadFileResponse>(
            `${BASE_URL}/upload`,
            formData,
            {
                params: path ? { path } : undefined,
                headers: {
                    'Content-Type': 'multipart/form-data'
                },
                onUploadProgress: (progressEvent) => {
                    if (onProgress && progressEvent.total) {
                        const percent = Math.round((progressEvent.loaded * 100) / progressEvent.total);
                        onProgress(percent);
                    }
                }
            }
        );
        return response.data;
    } catch (error) {
        console.error('Failed to upload file:', error);
        return null;
    }
}


/**
 * Create a new folder
 */
export async function createUserFolder(folderName: string, path?: string): Promise<boolean> {
    try {
        await api.post(`${BASE_URL}/create-folder`, {
            folder_name: folderName,
            path: path || ''
        });
        return true;
    } catch (error) {
        console.error('Failed to create folder:', error);
        return false;
    }
}

/**
 * Delete a file from user's data folder
 */
export async function deleteUserFile(filename: string, path?: string): Promise<boolean> {
    try {
        await api.delete(`${BASE_URL}/files/${encodeURIComponent(filename)}`, {
            params: path ? { path } : undefined
        });
        return true;
    } catch (error) {
        console.error('Failed to delete file:', error);
        return false;
    }
}

/**
 * Get user's data folder S3A path (for use in Spark)
 */
export async function getUserDataPath(): Promise<UserDataPathResponse | null> {
    try {
        const response = await api.get<UserDataPathResponse>(`${BASE_URL}/path`);
        return response.data;
    } catch (error) {
        console.error('Failed to get user data path:', error);
        return null;
    }
}

/**
 * Get download URL for a file
 */
export function getDownloadUrl(filename: string, path?: string): string {
    let url = `${BASE_URL}/files/${encodeURIComponent(filename)}/download`;
    if (path) {
        url += `?path=${encodeURIComponent(path)}`;
    }
    return url;
}

/**
 * Format file size for display
 */
export function formatFileSize(bytes: number): string {
    if (bytes === 0) return '0 B';
    const k = 1024;
    const sizes = ['B', 'KB', 'MB', 'GB', 'TB'];
    const i = Math.floor(Math.log(bytes) / Math.log(k));
    return parseFloat((bytes / Math.pow(k, i)).toFixed(2)) + ' ' + sizes[i];
}

export const notebookStorageService = {
    listUserFiles,
    uploadUserFile,
    createUserFolder,
    deleteUserFile,
    getUserDataPath,
    getDownloadUrl,
    formatFileSize
};

export default notebookStorageService;
