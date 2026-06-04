package handlers

import (
	"encoding/xml"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/rs/zerolog/log"

	"github.com/sparklabx/sparklabx/backend/internal/config"
	"github.com/sparklabx/sparklabx/backend/internal/database"
	"github.com/sparklabx/sparklabx/backend/internal/services"
)

// urlEncode percent-encodes a path segment for S3/MinIO REST URLs (keeps "/").
func urlEncode(s string) string {
	return strings.ReplaceAll(url.QueryEscape(s), "%2F", "/")
}

type StorageHandler struct {
	encryptionKey       []byte
	corsOrigins         []string
	minioEndpoint       string
	minioAccessKey      string
	minioSecretKey      string
	workspaceBucket     string   // single shared bucket; users isolated via prefix
	minioBucketVerified sync.Map // bucket name → struct{} once create-or-already-exists succeeded
}

func NewStorageHandler(cfg *config.Config) *StorageHandler {
	return &StorageHandler{
		encryptionKey:   []byte(cfg.JWTSecretKey),
		corsOrigins:     cfg.CORSOrigins,
		minioEndpoint:   cfg.MinIOEndpoint,
		minioAccessKey:  cfg.MinIOAccessKey,
		minioSecretKey:  cfg.MinIOSecretKey,
		workspaceBucket: cfg.MinIOWorkspaceBucket,
	}
}

func (h *StorageHandler) minioEnabled() bool {
	return h.minioEndpoint != "" && h.minioAccessKey != "" && h.workspaceBucket != ""
}

// EnsureWorkspaceBucket creates the single shared bucket on startup. Idempotent —
// MinIO returns 409 BucketAlreadyOwnedByYou if it exists, which the helper treats
// as success.
// EnsureWorkspaceBucket runs the bucket create-or-noop in a background loop
// until MinIO accepts the request. compose may start the backend before MinIO
// finishes its boot — synchronous create would fail and never retry.
func (h *StorageHandler) EnsureWorkspaceBucket() {
	if !h.minioEnabled() {
		return
	}
	go func() {
		for i := 0; ; i++ {
			if _, ok := h.minioBucketVerified.Load(h.workspaceBucket); ok {
				return
			}
			h.minioCreateBucket(h.workspaceBucket)
			if _, ok := h.minioBucketVerified.Load(h.workspaceBucket); ok {
				return
			}
			delay := time.Duration(1+i*2) * time.Second
			if delay > 30*time.Second {
				delay = 30 * time.Second
			}
			time.Sleep(delay)
		}
	}()
}

// userPrefix returns the caller's private storage prefix: "users/<username>/".
// Username comes from JWT (admin_username claim). Falls back to a DB lookup
// for legacy tokens that pre-date the claim.
func (h *StorageHandler) userPrefix(c *gin.Context) (string, error) {
	if u, ok := c.Get("admin_username"); ok {
		if s, ok := u.(string); ok && s != "" {
			return "users/" + s + "/", nil
		}
	}
	adminID, _ := c.Get("admin_id")
	adminStr, _ := adminID.(string)
	if adminStr == "" {
		return "", fmt.Errorf("no admin_id in token")
	}
	// Legacy fallback — token issued before admin_username was added.
	var username string
	if err := database.GetDB().QueryRow(
		"SELECT username FROM admins WHERE id = $1", adminStr,
	).Scan(&username); err != nil || username == "" {
		return "", fmt.Errorf("admin not found")
	}
	return "users/" + username + "/", nil
}

const publicStoragePrefix = "public/"

// resolveScope translates a virtual scope name ("my" or "public") into a real
// (bucket, prefix, canWrite) tuple. Used by MinIO browser endpoints to keep
// callers blind to the underlying single-bucket layout.
//   - "my"     → user's private space, R/W
//   - "public" → shared read-only space, write requires superadmin
func (h *StorageHandler) resolveScope(c *gin.Context, scope string) (bucket, prefix string, canWrite bool, err error) {
	switch scope {
	case "my":
		p, err := h.userPrefix(c)
		if err != nil {
			return "", "", false, err
		}
		return h.workspaceBucket, p, true, nil
	case "public":
		// Free-for-all share space — any authenticated user can R/W. Curated
		// (superadmin-only-write) is reserved for Phase B workspaces with roles.
		return h.workspaceBucket, publicStoragePrefix, true, nil
	default:
		return "", "", false, fmt.Errorf("unknown scope: %s", scope)
	}
}

// joinKey safely joins a scope prefix with a user-supplied relative path,
// rejecting absolute paths and "..". subPath/filename are TRUSTED from caller
// but validated to prevent prefix escape (e.g., user sending "../public/x").
func joinKey(prefix, subPath, filename string) (string, error) {
	combined := subPath + filename
	if strings.Contains(combined, "..") {
		return "", fmt.Errorf("path traversal not allowed")
	}
	// Strip leading "/" so callers can't bypass the prefix.
	subPath = strings.TrimPrefix(subPath, "/")
	filename = strings.TrimPrefix(filename, "/")
	return prefix + subPath + filename, nil
}

// stripPrefix removes a leading prefix from file keys so the frontend never
// sees the underlying users/<username>/ or public/ layout.
func stripPrefix(files []s3FileInfo, prefix string) []s3FileInfo {
	out := make([]s3FileInfo, 0, len(files))
	for _, f := range files {
		f.Key = strings.TrimPrefix(f.Key, prefix)
		out = append(out, f)
	}
	return out
}

// getStudentBucketInfo returns (workspaceBucket, userPrefix) for the caller.
// Name kept for backward-compat with legacy /api/v1/notebooks/storage/* routes.
func (h *StorageHandler) getStudentBucketInfo(c *gin.Context) (bucket, prefix string, err error) {
	p, err := h.userPrefix(c)
	if err != nil {
		return "", "", err
	}
	return h.workspaceBucket, p, nil
}

// GetUserDataPath returns the caller's storage URIs so the notebook init code
// can print them. The workspace bucket is created once at boot via
// EnsureWorkspaceBucket — no per-request creation needed.
// GET /api/v1/notebooks/storage/path
func (h *StorageHandler) GetUserDataPath(c *gin.Context) {
	bucket, prefix, err := h.getStudentBucketInfo(c)
	if err != nil {
		c.JSON(http.StatusOK, gin.H{"path": "", "bucket": "", "available": false})
		return
	}
	privatePath := fmt.Sprintf("s3a://%s/%s", bucket, prefix)
	publicPath := fmt.Sprintf("s3a://%s/%s", bucket, publicStoragePrefix)
	c.JSON(http.StatusOK, gin.H{
		// `path` kept for backward-compat with existing frontend; equals private path.
		"path":         privatePath,
		"private_path": privatePath,
		"public_path":  publicPath,
		"bucket":       bucket,
		"prefix":       prefix,
		"region":       "us-east-1",
		"endpoint":     h.minioEndpoint,
		"available":    true,
	})
}

// ListUserFiles lists files under the caller's private prefix.
// GET /api/v1/notebooks/storage/files?path=<subpath>
func (h *StorageHandler) ListUserFiles(c *gin.Context) {
	bucket, prefix, err := h.getStudentBucketInfo(c)
	if err != nil {
		c.JSON(http.StatusOK, gin.H{"files": []interface{}{}, "prefix": "", "path": ""})
		return
	}

	subPath := c.Query("path")
	fullPrefix, err := joinKey(prefix, subPath, "")
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	files, err := h.minioListObjects(bucket, fullPrefix)
	if err != nil {
		log.Error().Err(err).Str("bucket", bucket).Str("prefix", fullPrefix).Msg("failed to list MinIO objects")
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to list files"})
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"files":  stripPrefix(files, prefix),
		"prefix": subPath,
		"path":   fmt.Sprintf("s3a://%s/%s", bucket, fullPrefix),
	})
}

// UploadUserFile uploads a file to the caller's private prefix.
// POST /api/v1/notebooks/storage/upload
func (h *StorageHandler) UploadUserFile(c *gin.Context) {
	bucket, prefix, err := h.getStudentBucketInfo(c)
	if err != nil {
		c.JSON(http.StatusForbidden, gin.H{"error": err.Error()})
		return
	}

	file, header, err := c.Request.FormFile("file")
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "missing file"})
		return
	}
	defer file.Close()

	subPath := c.Query("path")
	key, err := joinKey(prefix, subPath, header.Filename)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	data, err := io.ReadAll(file)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to read file"})
		return
	}

	if err := h.minioPutObject(bucket, key, data, header.Header.Get("Content-Type")); err != nil {
		log.Error().Err(err).Str("key", key).Msg("failed to upload to MinIO")
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to upload file"})
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"success": true,
		"key":     strings.TrimPrefix(key, prefix),
		"name":    header.Filename,
		"size":    len(data),
		"path":    fmt.Sprintf("s3a://%s/%s", bucket, key),
	})
}

func (h *StorageHandler) CreateUserFolder(c *gin.Context) {
	bucket, prefix, err := h.getStudentBucketInfo(c)
	if err != nil {
		c.JSON(http.StatusForbidden, gin.H{"error": err.Error()})
		return
	}

	var req struct {
		FolderName string `json:"folder_name" binding:"required"`
		Path       string `json:"path"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "folder_name required"})
		return
	}

	key, err := joinKey(prefix, req.Path, req.FolderName+"/")
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	if err := h.minioPutObject(bucket, key, []byte{}, "application/x-directory"); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to create folder"})
		return
	}

	c.JSON(http.StatusOK, gin.H{"success": true})
}

func (h *StorageHandler) DeleteUserFile(c *gin.Context) {
	bucket, prefix, err := h.getStudentBucketInfo(c)
	if err != nil {
		c.JSON(http.StatusForbidden, gin.H{"error": err.Error()})
		return
	}

	filename := c.Param("filename")
	subPath := c.Query("path")
	key, err := joinKey(prefix, subPath, filename)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	if err := h.minioDeleteObject(bucket, key); err != nil {
		log.Error().Err(err).Str("key", key).Msg("failed to delete MinIO object")
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to delete file"})
		return
	}

	c.JSON(http.StatusOK, gin.H{"success": true})
}

func (h *StorageHandler) DownloadUserFile(c *gin.Context) {
	bucket, prefix, err := h.getStudentBucketInfo(c)
	if err != nil {
		c.JSON(http.StatusForbidden, gin.H{"error": err.Error()})
		return
	}

	filename := c.Param("filename")
	subPath := c.Query("path")
	key, err := joinKey(prefix, subPath, filename)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	data, contentType, err := h.minioGetObject(bucket, key)
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "file not found"})
		return
	}

	if contentType == "" {
		contentType = "application/octet-stream"
	}
	c.Header("Content-Disposition", fmt.Sprintf("attachment; filename=%q", filename))
	c.Data(http.StatusOK, contentType, data)
}

// ============================================================
// MinIO Browser Endpoints — scope-based ("my", "public")
// ============================================================
//
// Frontend addresses storage by SCOPE, not real bucket name. Routes look like
// `/api/v1/minio/buckets/my/objects?prefix=datasets/` and the handler
// translates "my" → workspace bucket + users/<username>/ prefix.

// MinIOListBuckets returns the virtual scopes available to the caller.
// Both scopes always exist; UI labels distinguish them. `canWrite` is hinted
// for the public scope so the UI can disable upload/delete for non-superadmin.
func (h *StorageHandler) MinIOListBuckets(c *gin.Context) {
	if !h.minioEnabled() {
		c.JSON(http.StatusOK, gin.H{"buckets": []interface{}{}, "available": false})
		return
	}
	c.JSON(http.StatusOK, gin.H{
		"buckets": []gin.H{
			{"name": "my", "display": "My Space", "can_write": true},
			{"name": "public", "display": "Public", "can_write": true},
		},
		"available": true,
	})
}

// MinIOListBucketsRaw is the original "list all buckets" — kept for potential
// future admin/superadmin debug screen but NOT wired to any route in lite mode.
func (h *StorageHandler) MinIOListBucketsRaw(c *gin.Context) {
	if !h.minioEnabled() {
		c.JSON(http.StatusOK, gin.H{"buckets": []interface{}{}, "available": false})
		return
	}

	parsed, _ := url.Parse(h.minioEndpoint)
	host := parsed.Host
	scheme := parsed.Scheme
	if scheme == "" {
		scheme = "http"
	}

	endpoint := fmt.Sprintf("%s://%s/", scheme, host)
	resp, err := minioSignedRequest("GET", endpoint, host, h.minioAccessKey, h.minioSecretKey, nil, "")
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to connect to MinIO"})
		return
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to read MinIO response"})
		return
	}
	if resp.StatusCode != 200 {
		c.JSON(http.StatusInternalServerError, gin.H{"error": fmt.Sprintf("MinIO returned %d", resp.StatusCode)})
		return
	}

	var result struct {
		Buckets struct {
			Bucket []struct {
				Name         string `xml:"Name"`
				CreationDate string `xml:"CreationDate"`
			} `xml:"Bucket"`
		} `xml:"Buckets"`
	}
	if err := xml.Unmarshal(body, &result); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to parse MinIO response"})
		return
	}

	type bucketInfo struct {
		Name         string `json:"name"`
		CreationDate string `json:"creation_date,omitempty"`
	}
	buckets := []bucketInfo{}
	for _, b := range result.Buckets.Bucket {
		buckets = append(buckets, bucketInfo{Name: b.Name, CreationDate: b.CreationDate})
	}

	c.JSON(http.StatusOK, gin.H{"buckets": buckets, "available": true})
}

// MinIOCreateBucket — disabled. There's a single workspace bucket, auto-created
// at backend startup. Users isolate via prefix, not separate buckets.
func (h *StorageHandler) MinIOCreateBucket(c *gin.Context) {
	c.JSON(http.StatusForbidden, gin.H{"error": "creating buckets is disabled"})
}

// MinIODeleteBucket — disabled. Same reason as create.
func (h *StorageHandler) MinIODeleteBucket(c *gin.Context) {
	c.JSON(http.StatusForbidden, gin.H{"error": "deleting buckets is disabled"})
}

// MinIOListObjects lists objects under the caller's scope ("my" or "public").
// The full real-bucket prefix is hidden from the response — client sees keys
// relative to the scope root (e.g. "datasets/x.csv" not "users/trung.nt/datasets/x.csv").
func (h *StorageHandler) MinIOListObjects(c *gin.Context) {
	if !h.minioEnabled() {
		c.JSON(http.StatusOK, gin.H{"files": []interface{}{}, "available": false})
		return
	}
	scope := c.Param("bucket")
	bucket, scopePrefix, _, err := h.resolveScope(c, scope)
	if err != nil {
		c.JSON(http.StatusForbidden, gin.H{"error": err.Error()})
		return
	}
	relPrefix := c.Query("prefix")
	fullPrefix, err := joinKey(scopePrefix, relPrefix, "")
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	files, err := h.minioListObjects(bucket, fullPrefix)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{
		"files":  stripPrefix(files, scopePrefix),
		"prefix": relPrefix,
		"bucket": scope,
	})
}

// MinIOCreateFolder creates a folder marker (empty object with trailing "/") at
// the caller's scope-rooted relative key.
func (h *StorageHandler) MinIOCreateFolder(c *gin.Context) {
	if !h.minioEnabled() {
		c.JSON(http.StatusBadRequest, gin.H{"error": "MinIO not configured"})
		return
	}
	scope := c.Param("bucket")
	bucket, scopePrefix, canWrite, err := h.resolveScope(c, scope)
	if err != nil {
		c.JSON(http.StatusForbidden, gin.H{"error": err.Error()})
		return
	}
	if !canWrite {
		c.JSON(http.StatusForbidden, gin.H{"error": "read-only scope"})
		return
	}
	var req struct {
		Key string `json:"key" binding:"required"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "key required"})
		return
	}
	fullKey, err := joinKey(scopePrefix, req.Key, "")
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	if err := h.minioPutObject(bucket, fullKey, []byte{}, "application/x-directory"); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to create folder"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"success": true, "key": req.Key})
}

// MinIOUploadObject uploads a file under the caller's scope-rooted relative path.
func (h *StorageHandler) MinIOUploadObject(c *gin.Context) {
	if !h.minioEnabled() {
		c.JSON(http.StatusBadRequest, gin.H{"error": "MinIO not configured"})
		return
	}
	scope := c.Param("bucket")
	bucket, scopePrefix, canWrite, err := h.resolveScope(c, scope)
	if err != nil {
		c.JSON(http.StatusForbidden, gin.H{"error": err.Error()})
		return
	}
	if !canWrite {
		c.JSON(http.StatusForbidden, gin.H{"error": "read-only scope"})
		return
	}

	file, header, err := c.Request.FormFile("file")
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "missing file"})
		return
	}
	defer file.Close()

	subPath := c.PostForm("path")
	if subPath == "" {
		subPath = c.Query("path")
	}
	fullKey, err := joinKey(scopePrefix, subPath, header.Filename)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	data, err := io.ReadAll(file)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to read file"})
		return
	}

	ct := header.Header.Get("Content-Type")
	if ct == "" {
		ct = "application/octet-stream"
	}

	if err := h.minioPutObject(bucket, fullKey, data, ct); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to upload to MinIO"})
		return
	}

	relKey := strings.TrimPrefix(fullKey, scopePrefix)
	c.JSON(http.StatusOK, gin.H{"success": true, "key": relKey, "name": header.Filename, "size": len(data)})
}

// MinIODownloadObject downloads a file from the caller's scope.
// Supports ?preview=true to only fetch first 1MB.
func (h *StorageHandler) MinIODownloadObject(c *gin.Context) {
	if !h.minioEnabled() {
		c.JSON(http.StatusBadRequest, gin.H{"error": "MinIO not configured"})
		return
	}
	scope := c.Param("bucket")
	bucket, scopePrefix, _, err := h.resolveScope(c, scope)
	if err != nil {
		c.JSON(http.StatusForbidden, gin.H{"error": err.Error()})
		return
	}
	relKey := c.Query("key")
	if relKey == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "key required"})
		return
	}
	fullKey, err := joinKey(scopePrefix, relKey, "")
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	data, ct, err := h.minioGetObject(bucket, fullKey)
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "file not found"})
		return
	}

	// Preview mode: limit to 1MB
	if c.Query("preview") == "true" && len(data) > 1024*1024 {
		data = data[:1024*1024]
	}

	if ct == "" {
		ct = "application/octet-stream"
	}

	filename := fullKey[strings.LastIndex(fullKey, "/")+1:]
	c.Header("Content-Disposition", fmt.Sprintf("inline; filename=%q", filename))
	c.Data(http.StatusOK, ct, data)
}

// MinIODeleteObject deletes an object (or recursively, a folder) under the
// caller's scope.
func (h *StorageHandler) MinIODeleteObject(c *gin.Context) {
	if !h.minioEnabled() {
		c.JSON(http.StatusBadRequest, gin.H{"error": "MinIO not configured"})
		return
	}
	scope := c.Param("bucket")
	bucket, scopePrefix, canWrite, err := h.resolveScope(c, scope)
	if err != nil {
		c.JSON(http.StatusForbidden, gin.H{"error": err.Error()})
		return
	}
	if !canWrite {
		c.JSON(http.StatusForbidden, gin.H{"error": "read-only scope"})
		return
	}
	relKey := c.Query("key")
	if relKey == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "key required"})
		return
	}
	fullKey, err := joinKey(scopePrefix, relKey, "")
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	// If key is a folder (prefix), recursively delete all contents
	if strings.HasSuffix(fullKey, "/") {
		if err := h.minioDeleteRecursive(bucket, fullKey); err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to delete folder contents"})
			return
		}
		c.JSON(http.StatusOK, gin.H{"success": true})
		return
	}

	if err := h.minioDeleteObject(bucket, fullKey); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to delete"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"success": true})
}

// ============================================================
// MinIO operations — S3-compatible, reuses SigV4 signing
// ============================================================

type s3FileInfo struct {
	Key          string `json:"key"`
	Name         string `json:"name"`
	Size         int64  `json:"size"`
	IsFolder     bool   `json:"is_folder"`
	LastModified string `json:"last_modified,omitempty"`
}

// minioPutObject uploads an object to MinIO (path-style: endpoint/bucket/key)
func (h *StorageHandler) minioPutObject(bucket, key string, data []byte, contentType string) error {
	if !h.minioEnabled() {
		return nil
	}

	// Ensure bucket exists (create if not)
	h.minioCreateBucket(bucket)

	// Parse endpoint to get host
	parsed, _ := url.Parse(h.minioEndpoint)
	host := parsed.Host
	scheme := parsed.Scheme
	if scheme == "" {
		scheme = "http"
	}

	endpoint := fmt.Sprintf("%s://%s/%s/%s", scheme, host, bucket, urlEncode(key))

	resp, err := minioSignedRequest("PUT", endpoint, host, h.minioAccessKey, h.minioSecretKey, data, contentType)
	if err != nil {
		log.Warn().Err(err).Str("key", key).Msg("MinIO PutObject failed")
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 300 {
		body, _ := io.ReadAll(resp.Body)
		log.Warn().Int("status", resp.StatusCode).Str("body", string(body)).Msg("MinIO PutObject error")
		return fmt.Errorf("MinIO PutObject returned %d", resp.StatusCode)
	}
	log.Info().Str("bucket", bucket).Str("key", key).Msg("MinIO: object synced")
	return nil
}

// minioDeleteObject deletes an object from MinIO
// minioDeleteRecursive deletes ALL objects under a prefix (flat listing, no delimiter)
// so folder markers and deeply nested objects are all caught.
func (h *StorageHandler) minioDeleteRecursive(bucket, prefix string) error {
	keys, err := h.minioListAllKeys(bucket, prefix)
	if err != nil {
		log.Error().Err(err).Str("prefix", prefix).Msg("minioDeleteRecursive: list failed")
		return err
	}
	var lastErr error
	for _, key := range keys {
		if err := h.minioDeleteObject(bucket, key); err != nil {
			lastErr = err
		}
	}
	return lastErr
}

// minioListAllKeys lists ALL object keys under a prefix (no delimiter, handles pagination).
func (h *StorageHandler) minioListAllKeys(bucket, prefix string) ([]string, error) {
	parsed, _ := url.Parse(h.minioEndpoint)
	host := parsed.Host
	scheme := parsed.Scheme
	if scheme == "" {
		scheme = "http"
	}

	var allKeys []string
	continuationToken := ""

	for {
		query := fmt.Sprintf("list-type=2&prefix=%s", urlEncode(prefix))
		if continuationToken != "" {
			query += "&continuation-token=" + urlEncode(continuationToken)
		}
		endpoint := fmt.Sprintf("%s://%s/%s/?%s", scheme, host, bucket, query)

		resp, err := minioSignedRequest("GET", endpoint, host, h.minioAccessKey, h.minioSecretKey, nil, "application/xml")
		if err != nil {
			return nil, err
		}
		body, err := io.ReadAll(resp.Body)
		resp.Body.Close()
		if err != nil {
			return nil, fmt.Errorf("MinIO list read body: %w", err)
		}
		if resp.StatusCode != 200 {
			return nil, fmt.Errorf("MinIO list returned %d: %s", resp.StatusCode, string(body))
		}

		var result struct {
			Contents []struct {
				Key string `xml:"Key"`
			} `xml:"Contents"`
			IsTruncated        bool   `xml:"IsTruncated"`
			NextContinuationToken string `xml:"NextContinuationToken"`
		}
		if err := xml.Unmarshal(body, &result); err != nil {
			return nil, fmt.Errorf("MinIO list parse XML: %w", err)
		}

		for _, obj := range result.Contents {
			allKeys = append(allKeys, obj.Key)
		}

		if !result.IsTruncated || result.NextContinuationToken == "" {
			break
		}
		continuationToken = result.NextContinuationToken
	}

	return allKeys, nil
}

func (h *StorageHandler) minioDeleteObject(bucket, key string) error {
	if !h.minioEnabled() {
		return nil
	}

	parsed, _ := url.Parse(h.minioEndpoint)
	host := parsed.Host
	scheme := parsed.Scheme
	if scheme == "" {
		scheme = "http"
	}

	endpoint := fmt.Sprintf("%s://%s/%s/%s", scheme, host, bucket, urlEncode(key))

	resp, err := minioSignedRequest("DELETE", endpoint, host, h.minioAccessKey, h.minioSecretKey, nil, "")
	if err != nil {
		log.Warn().Err(err).Str("key", key).Msg("MinIO DeleteObject failed")
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		body, _ := io.ReadAll(resp.Body)
		log.Warn().Int("status", resp.StatusCode).Str("key", key).Str("body", string(body)).Msg("MinIO DeleteObject error")
		return fmt.Errorf("MinIO DELETE %s returned %d", key, resp.StatusCode)
	}
	return nil
}

// minioCreateBucket creates a bucket on MinIO if it doesn't exist.
// Memoized per-bucket so subsequent calls in the same process skip the network
// RTT (~30-100ms saved per list/upload).
func (h *StorageHandler) minioCreateBucket(bucket string) {
	if _, ok := h.minioBucketVerified.Load(bucket); ok {
		return
	}
	parsed, _ := url.Parse(h.minioEndpoint)
	host := parsed.Host
	scheme := parsed.Scheme
	if scheme == "" {
		scheme = "http"
	}

	endpoint := fmt.Sprintf("%s://%s/%s/", scheme, host, bucket)

	resp, err := minioSignedRequest("PUT", endpoint, host, h.minioAccessKey, h.minioSecretKey, nil, "")
	if err != nil {
		log.Warn().Err(err).Str("bucket", bucket).Msg("MinIO CreateBucket failed")
		return
	}
	defer resp.Body.Close()
	// 200 = created, 409 = already exists — both OK
	if resp.StatusCode == 200 || resp.StatusCode == 409 {
		h.minioBucketVerified.Store(bucket, struct{}{})
	}
}

// minioListObjects lists objects in a MinIO bucket
func (h *StorageHandler) minioListObjects(bucket, prefix string) ([]s3FileInfo, error) {
	if !h.minioEnabled() {
		return nil, fmt.Errorf("minio not configured")
	}

	parsed, _ := url.Parse(h.minioEndpoint)
	host := parsed.Host
	scheme := parsed.Scheme
	if scheme == "" {
		scheme = "http"
	}

	query := fmt.Sprintf("list-type=2&prefix=%s&delimiter=/", urlEncode(prefix))
	endpoint := fmt.Sprintf("%s://%s/%s/?%s", scheme, host, bucket, query)

	resp, err := minioSignedRequest("GET", endpoint, host, h.minioAccessKey, h.minioSecretKey, nil, "application/xml")
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("MinIO ListObjects read body: %w", err)
	}
	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("MinIO ListObjects returned %d: %s", resp.StatusCode, string(body))
	}

	var result struct {
		CommonPrefixes []struct {
			Prefix string `xml:"Prefix"`
		} `xml:"CommonPrefixes"`
		Contents []struct {
			Key          string `xml:"Key"`
			Size         int64  `xml:"Size"`
			LastModified string `xml:"LastModified"`
		} `xml:"Contents"`
	}
	if err := xml.Unmarshal(body, &result); err != nil {
		return nil, fmt.Errorf("MinIO ListObjects parse XML: %w", err)
	}

	var files []s3FileInfo
	for _, cp := range result.CommonPrefixes {
		name := strings.TrimPrefix(cp.Prefix, prefix)
		name = strings.TrimSuffix(name, "/")
		if name != "" {
			files = append(files, s3FileInfo{Key: cp.Prefix, Name: name, IsFolder: true})
		}
	}
	for _, obj := range result.Contents {
		name := strings.TrimPrefix(obj.Key, prefix)
		if name == "" || strings.HasSuffix(name, "/") {
			continue
		}
		files = append(files, s3FileInfo{Key: obj.Key, Name: name, Size: obj.Size, LastModified: obj.LastModified})
	}
	if files == nil {
		files = []s3FileInfo{}
	}
	return files, nil
}

// minioGetObject downloads an object from MinIO
func (h *StorageHandler) minioGetObject(bucket, key string) ([]byte, string, error) {
	if !h.minioEnabled() {
		return nil, "", fmt.Errorf("minio not configured")
	}

	parsed, _ := url.Parse(h.minioEndpoint)
	host := parsed.Host
	scheme := parsed.Scheme
	if scheme == "" {
		scheme = "http"
	}

	endpoint := fmt.Sprintf("%s://%s/%s/%s", scheme, host, bucket, urlEncode(key))

	resp, err := minioSignedRequest("GET", endpoint, host, h.minioAccessKey, h.minioSecretKey, nil, "")
	if err != nil {
		return nil, "", err
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		return nil, "", fmt.Errorf("MinIO GetObject returned %d", resp.StatusCode)
	}

	data, err := io.ReadAll(resp.Body)
	return data, resp.Header.Get("Content-Type"), err
}

// minioSignedRequest sends a SigV4-signed request to MinIO (region=us-east-1)
func minioSignedRequest(method, endpoint, host, accessKey, secretKey string, body []byte, contentType string) (*http.Response, error) {
	region := "us-east-1" // MinIO default region

	now := time.Now().UTC()
	datestamp := now.Format("20060102")
	amzDate := now.Format("20060102T150405Z")
	credentialScope := fmt.Sprintf("%s/%s/s3/aws4_request", datestamp, region)

	bodyHash := services.Sha256Hex(body)

	ct := contentType
	if ct == "" {
		ct = "application/octet-stream"
	}

	canonicalHeaders := fmt.Sprintf("content-type:%s\nhost:%s\nx-amz-content-sha256:%s\nx-amz-date:%s\n",
		ct, host, bodyHash, amzDate)
	signedHeaders := "content-type;host;x-amz-content-sha256;x-amz-date"

	parsedURL, _ := url.Parse(endpoint)
	canonicalURI := parsedURL.Path
	if canonicalURI == "" {
		canonicalURI = "/"
	}
	// Build canonical query string (sorted params for SigV4)
	queryParams := parsedURL.Query()
	var sortedKeys []string
	for k := range queryParams {
		sortedKeys = append(sortedKeys, k)
	}
	sort.Strings(sortedKeys)
	var canonicalQueryParts []string
	for _, k := range sortedKeys {
		for _, v := range queryParams[k] {
			canonicalQueryParts = append(canonicalQueryParts, fmt.Sprintf("%s=%s", url.QueryEscape(k), url.QueryEscape(v)))
		}
	}
	canonicalQueryString := strings.Join(canonicalQueryParts, "&")

	canonicalRequest := fmt.Sprintf("%s\n%s\n%s\n%s\n%s\n%s",
		method, canonicalURI, canonicalQueryString, canonicalHeaders, signedHeaders, bodyHash)

	stringToSign := fmt.Sprintf("AWS4-HMAC-SHA256\n%s\n%s\n%s",
		amzDate, credentialScope, services.Sha256Hex([]byte(canonicalRequest)))

	kDate := services.HmacSign([]byte("AWS4"+secretKey), []byte(datestamp))
	kRegion := services.HmacSign(kDate, []byte(region))
	kService := services.HmacSign(kRegion, []byte("s3"))
	kSigning := services.HmacSign(kService, []byte("aws4_request"))

	signature := fmt.Sprintf("%x", services.HmacSign(kSigning, []byte(stringToSign)))

	authHeader := fmt.Sprintf("AWS4-HMAC-SHA256 Credential=%s/%s, SignedHeaders=%s, Signature=%s",
		accessKey, credentialScope, signedHeaders, signature)

	var reqBody io.Reader
	if body != nil {
		reqBody = strings.NewReader(string(body))
	}
	req, err := http.NewRequest(method, endpoint, reqBody)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", ct)
	req.Header.Set("Host", host)
	req.Header.Set("X-Amz-Date", amzDate)
	req.Header.Set("X-Amz-Content-Sha256", bodyHash)
	req.Header.Set("Authorization", authHeader)

	return http.DefaultClient.Do(req)
}
