// Package services — aws_sigv4.go implements AWS Signature Version 4 helpers.
//
// SparkLabX doesn't run on AWS; this file is needed because MinIO (and any
// S3-compatible store: R2, Backblaze, Garage, etc.) uses the same SigV4
// protocol as AWS S3 to authenticate REST requests. Every call the backend
// makes to MinIO — list / put / get / delete object — is signed via these
// helpers.
package services

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"net/url"
	"os"
)

// Sha256Hex returns hex-encoded SHA256(data) for SigV4 canonical hashing.
func Sha256Hex(data []byte) string {
	h := sha256.Sum256(data)
	return hex.EncodeToString(h[:])
}

// HmacSign returns HMAC-SHA256(key, data) raw bytes for SigV4 key derivation.
func HmacSign(key, data []byte) []byte {
	m := hmac.New(sha256.New, key)
	m.Write(data)
	return m.Sum(nil)
}

// AwsSessionToken returns AWS_SESSION_TOKEN if set (used for AWS sandbox /
// AssumeRole creds that come with a temporary security token).
func AwsSessionToken() string { return os.Getenv("AWS_SESSION_TOKEN") }

// AwsSigV4SessionTokenParts returns the pieces to splice into a SigV4 canonical
// request when AWS_SESSION_TOKEN env is set. All return values are empty when
// unset, so callers don't need to branch.
//
// canonicalLine    — append to canonicalHeaders before computing the signature
// signedSuffix     — append to signedHeaders (";x-amz-security-token")
// token            — pass to ApplySessionTokenHeader() after building the request
func AwsSigV4SessionTokenParts() (canonicalLine, signedSuffix, token string) {
	return SigV4SessionTokenParts(AwsSessionToken())
}

// SigV4SessionTokenParts is AwsSigV4SessionTokenParts with an explicit token
// (for callers signing with creds passed in from a request body, not env).
func SigV4SessionTokenParts(token string) (canonicalLine, signedSuffix, t string) {
	if token == "" {
		return "", "", ""
	}
	return "x-amz-security-token:" + token + "\n", ";x-amz-security-token", token
}

// ApplySessionTokenHeader sets the X-Amz-Security-Token header on req if token
// is non-empty. No-op otherwise so callers can call unconditionally.
func ApplySessionTokenHeader(req *http.Request, token string) {
	if token != "" {
		req.Header.Set("X-Amz-Security-Token", token)
	}
}

// AddSessionTokenToPresignParams adds X-Amz-Security-Token to query params for
// presigned URLs. No-op when no session token is configured (env-based).
func AddSessionTokenToPresignParams(params url.Values) {
	if token := AwsSessionToken(); token != "" {
		params.Set("X-Amz-Security-Token", token)
	}
}
