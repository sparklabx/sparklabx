package services

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	cryptorand "crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/url"
	"strings"

	"github.com/minio/madmin-go/v3"
)

// MinIOIAM provisions per-user IAM credentials and scoped policies on a MinIO
// server. Each user gets:
//   - a MinIO user account named after their app username slug
//   - a randomly-generated secret stored AES-GCM encrypted in the DB
//   - an inline policy granting R/W only on `<bucket>/users/<slug>/*` and
//     `<bucket>/public/*`. All other prefixes are implicitly denied.
//
// The kernel pod injects these per-user creds as AWS_ACCESS_KEY_ID /
// AWS_SECRET_ACCESS_KEY so spark.read.csv("s3a://<bucket>/users/other/...")
// gets a 403 at the MinIO layer — true isolation, not app-layer-only.
type MinIOIAM struct {
	client        *madmin.AdminClient
	bucket        string
	encryptionKey []byte // 32-byte key derived from JWT secret
}

// NewMinIOIAM returns nil (with nil error) if MinIO admin is not configured —
// callers should treat that as "IAM disabled" and skip per-user provisioning.
func NewMinIOIAM(endpoint, accessKey, secretKey, bucket, encryptionSecret string) (*MinIOIAM, error) {
	if endpoint == "" || accessKey == "" || secretKey == "" {
		return nil, nil
	}
	u, err := url.Parse(endpoint)
	if err != nil {
		return nil, fmt.Errorf("parse minio endpoint: %w", err)
	}
	host := u.Host
	if host == "" {
		host = endpoint
	}
	secure := u.Scheme == "https"
	c, err := madmin.New(host, accessKey, secretKey, secure)
	if err != nil {
		return nil, fmt.Errorf("madmin.New: %w", err)
	}
	// Derive a 32-byte AES key from the encryption secret (typically JWT key).
	sum := sha256.Sum256([]byte(encryptionSecret))
	return &MinIOIAM{client: c, bucket: bucket, encryptionKey: sum[:]}, nil
}

// GenerateSecret returns a 32-char hex string suitable as an S3 secret key.
// MinIO accepts secret keys 8..40 chars; 32 hex = 128 bits of entropy.
func GenerateSecret() (string, error) {
	buf := make([]byte, 16)
	if _, err := cryptorand.Read(buf); err != nil {
		return "", err
	}
	return hex.EncodeToString(buf), nil
}

// EncryptSecret AES-GCM-encrypts a plaintext secret with the handler key.
// The output is `base64(nonce || ciphertext)` — single string, DB-friendly.
func (m *MinIOIAM) EncryptSecret(plaintext string) (string, error) {
	block, err := aes.NewCipher(m.encryptionKey)
	if err != nil {
		return "", err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return "", err
	}
	nonce := make([]byte, gcm.NonceSize())
	if _, err := cryptorand.Read(nonce); err != nil {
		return "", err
	}
	ciphertext := gcm.Seal(nil, nonce, []byte(plaintext), nil)
	out := append(nonce, ciphertext...)
	return base64.StdEncoding.EncodeToString(out), nil
}

// DecryptSecret reverses EncryptSecret.
func (m *MinIOIAM) DecryptSecret(encoded string) (string, error) {
	data, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil {
		return "", err
	}
	block, err := aes.NewCipher(m.encryptionKey)
	if err != nil {
		return "", err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return "", err
	}
	if len(data) < gcm.NonceSize() {
		return "", fmt.Errorf("ciphertext too short")
	}
	nonce, ciphertext := data[:gcm.NonceSize()], data[gcm.NonceSize():]
	plaintext, err := gcm.Open(nil, nonce, ciphertext, nil)
	if err != nil {
		return "", err
	}
	return string(plaintext), nil
}

// EnsureUser creates the MinIO IAM user and attaches the per-user scoped policy.
// Idempotent — re-running for an existing user updates their secret (so we
// can rotate or restore from DB after a MinIO data wipe).
func (m *MinIOIAM) EnsureUser(ctx context.Context, slug, secret string) error {
	if err := m.client.AddUser(ctx, slug, secret); err != nil {
		return fmt.Errorf("AddUser %q: %w", slug, err)
	}
	policyName := userPolicyName(slug)
	policyJSON, err := buildUserPolicy(m.bucket, slug)
	if err != nil {
		return err
	}
	if err := m.client.AddCannedPolicy(ctx, policyName, policyJSON); err != nil {
		return fmt.Errorf("AddCannedPolicy %q: %w", policyName, err)
	}
	if _, err := m.client.AttachPolicy(ctx, madmin.PolicyAssociationReq{
		Policies: []string{policyName},
		User:     slug,
	}); err != nil {
		// AttachPolicy returns an error if the policy is already attached.
		// Tolerate that — re-attachment is a no-op semantically.
		if !strings.Contains(err.Error(), "already") {
			return fmt.Errorf("AttachPolicy %q to %q: %w", policyName, slug, err)
		}
	}
	return nil
}

// RemoveUser deletes the IAM user and policy. Used on admin deletion.
func (m *MinIOIAM) RemoveUser(ctx context.Context, slug string) error {
	_ = m.client.RemoveCannedPolicy(ctx, userPolicyName(slug))
	return m.client.RemoveUser(ctx, slug)
}

func userPolicyName(slug string) string {
	return "user-" + slug + "-policy"
}

// buildUserPolicy creates an inline policy JSON granting:
//   - full R/W on bucket/users/<slug>/*
//   - full R/W on bucket/public/*  (free-for-all share space)
//   - list bucket scoped to those two prefixes (so navigation works)
//
// Everything outside those prefixes is implicitly denied. Bucket-level ops
// (create/delete bucket) are not granted.
func buildUserPolicy(bucket, slug string) ([]byte, error) {
	policy := map[string]any{
		"Version": "2012-10-17",
		"Statement": []map[string]any{
			{
				"Effect": "Allow",
				"Action": []string{
					"s3:GetObject",
					"s3:PutObject",
					"s3:DeleteObject",
					"s3:GetObjectAttributes",
					"s3:ListMultipartUploadParts",
					"s3:AbortMultipartUpload",
				},
				"Resource": []string{
					fmt.Sprintf("arn:aws:s3:::%s/users/%s/*", bucket, slug),
					fmt.Sprintf("arn:aws:s3:::%s/public/*", bucket),
				},
			},
			{
				// ListBucket is scoped via prefix — caller can only see entries
				// under their own users/<slug>/* or public/*.
				"Effect":   "Allow",
				"Action":   []string{"s3:ListBucket"},
				"Resource": fmt.Sprintf("arn:aws:s3:::%s", bucket),
				"Condition": map[string]any{
					"StringLike": map[string]any{
						"s3:prefix": []string{
							fmt.Sprintf("users/%s/*", slug),
							fmt.Sprintf("users/%s", slug),
							"public/*",
							"public",
						},
					},
				},
			},
			{
				// GetBucketLocation cannot carry an s3:prefix condition (MinIO
				// rejects the policy outright); grant unconditionally on the
				// single workspace bucket — leaks only the bucket region string.
				"Effect":   "Allow",
				"Action":   []string{"s3:GetBucketLocation"},
				"Resource": fmt.Sprintf("arn:aws:s3:::%s", bucket),
			},
			{
				"Effect":   "Allow",
				"Action":   []string{"s3:ListAllMyBuckets"},
				"Resource": "arn:aws:s3:::*",
			},
		},
	}
	return json.Marshal(policy)
}
