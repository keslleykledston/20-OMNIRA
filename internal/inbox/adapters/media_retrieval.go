package adapters

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	platformdb "github.com/omnira/omnira/internal/platform/db"
	tenancydomain "github.com/omnira/omnira/internal/tenancy/domain"
)

const maxMediaBytes = 25 * 1024 * 1024 // 25 MiB

// MediaRetriever handles bounded, secure inbound media retrieval from WAHA.
// Media references (provider URLs) are internal-only and never exposed to clients.
type MediaRetriever struct {
	pool              *pgxpool.Pool
	trustedWahaOrigin string // scheme://host:port from OMNIRA_WAHA_BASE_URL
	httpClient        *http.Client
}

// NewMediaRetriever creates a retriever configured with a trusted WAHA origin.
// origin should be the exact WAHA base URL (e.g., "http://waha:3000").
func NewMediaRetriever(pool *pgxpool.Pool, origin string) (*MediaRetriever, error) {
	u, err := url.Parse(origin)
	if err != nil {
		return nil, fmt.Errorf("invalid trusted WAHA origin: %w", err)
	}
	// Require explicit scheme and host. Do not accept "waha" or bare hostnames.
	if u.Scheme == "" || u.Host == "" {
		return nil, fmt.Errorf("trusted WAHA origin must include scheme and host: %q", origin)
	}
	// Normalize: remove any userinfo, path, query, fragment.
	trustedOrigin := fmt.Sprintf("%s://%s", u.Scheme, u.Host)

	// Custom HTTP client with redirects disabled.
	client := &http.Client{
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			// Disable automatic redirect following. This prevents:
			// - Server-side request forgery (SSRF) to foreign origins
			// - Phishing redirects to attacker-controlled servers
			return http.ErrUseLastResponse
		},
		Timeout: 30,
	}

	return &MediaRetriever{
		pool:              pool,
		trustedWahaOrigin: trustedOrigin,
		httpClient:        client,
	}, nil
}

// Retrieve fetches media for a message, returning safe bytes or error.
// The media reference (MediaRef) is looked up from the database with RLS
// enforcement via the message lookup; the URL itself is untrusted.
//
// Returns:
//   - (body, mimeType, error) on success
//   - (nil, "", error) on failure (including remote errors)
//
// Security:
//   - Message must be readable by the current tenant (RLS)
//   - MediaRef origin is validated against exact configured WAHA URL
//   - Userinfo (credentials) in URL is rejected
//   - HTTP redirects are NOT followed
//   - Content-Length >25 MiB is rejected
//   - Unknown Content-Length triggers bounded read
//   - MIME type is sniffed from bytes (declared type is untrusted)
func (r *MediaRetriever) Retrieve(ctx context.Context, tenantID, messageID uuid.UUID) ([]byte, string, error) {
	// Look up message with RLS enforcement.
	var mediaRef *string
	err := platformdb.QuerierFromContext(ctx, r.pool).QueryRow(ctx, `
		SELECT m.media_ref FROM messages m
		WHERE m.tenant_id=$1 AND m.id=$2
	`, tenantID, messageID).Scan(&mediaRef)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, "", fmt.Errorf("message not found")
	}
	if err != nil {
		return nil, "", fmt.Errorf("message lookup failed: %w", err)
	}
	if mediaRef == nil || *mediaRef == "" {
		return nil, "", fmt.Errorf("message has no media")
	}

	// Validate and normalize media URL.
	body, mimeType, err := r.retrieveAndSniff(ctx, *mediaRef)
	if err != nil {
		return nil, "", err
	}
	return body, mimeType, nil
}

func (r *MediaRetriever) retrieveAndSniff(ctx context.Context, mediaRef string) ([]byte, string, error) {
	// Parse media URL.
	u, err := url.Parse(mediaRef)
	if err != nil {
		return nil, "", fmt.Errorf("invalid media URL: %w", err)
	}

	// Reject userinfo (credentials embedded in URL).
	if u.User != nil {
		return nil, "", fmt.Errorf("media URL contains userinfo (credentials)")
	}

	// Validate origin: scheme and host:port must match exactly.
	mediaOrigin := fmt.Sprintf("%s://%s", u.Scheme, u.Host)
	if mediaOrigin != r.trustedWahaOrigin {
		return nil, "", fmt.Errorf("media origin mismatch: %q != %q", mediaOrigin, r.trustedWahaOrigin)
	}

	// GET media from WAHA.
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, mediaRef, nil)
	if err != nil {
		return nil, "", fmt.Errorf("failed to create media request: %w", err)
	}
	req.Header.Set("Accept", "*/*")
	req.Header.Set("User-Agent", "omnira/media-retriever")

	resp, err := r.httpClient.Do(req)
	if err != nil {
		// Check for redirect response (ErrUseLastResponse).
		if errors.Is(err, http.ErrUseLastResponse) {
			// Client tried to redirect; we disabled it.
			if resp != nil && (resp.StatusCode >= 300 && resp.StatusCode < 400) {
				return nil, "", fmt.Errorf("media redirect to foreign origin not allowed")
			}
		}
		return nil, "", fmt.Errorf("media retrieval failed: %w", err)
	}
	defer resp.Body.Close()

	// Check response status.
	if resp.StatusCode != http.StatusOK {
		// Discard body to avoid leaking provider errors.
		_, _ = io.ReadAll(resp.Body)
		if resp.StatusCode == http.StatusNotFound {
			return nil, "", fmt.Errorf("media not found")
		}
		if resp.StatusCode >= 500 {
			return nil, "", fmt.Errorf("media provider error")
		}
		return nil, "", fmt.Errorf("media retrieval returned %d", resp.StatusCode)
	}

	// Validate Content-Length if present.
	if resp.ContentLength > 0 {
		if resp.ContentLength > int64(maxMediaBytes) {
			return nil, "", fmt.Errorf("media too large (%d > %d bytes)", resp.ContentLength, maxMediaBytes)
		}
	}

	// Read and bound media bytes with overflow detection.
	// LimitReader stops at maxMediaBytes but doesn't error; we read maxMediaBytes+1
	// to detect if upstream has more data beyond limit.
	limitedReader := io.LimitReader(resp.Body, int64(maxMediaBytes+1))
	buf := bytes.NewBuffer(make([]byte, 0, 1024*1024)) // 1 MiB initial capacity
	if _, err := buf.ReadFrom(limitedReader); err != nil {
		return nil, "", fmt.Errorf("failed to read media: %w", err)
	}

	// Check if size exceeded limit.
	if buf.Len() > maxMediaBytes {
		return nil, "", fmt.Errorf("media exceeds %d byte limit", maxMediaBytes)
	}

	// Sniff MIME type from bytes.
	contentType := sniffMimeType(buf.Bytes(), resp.Header.Get("Content-Type"))

	// Verify content is safe to inline or download.
	if !isSafeContent(contentType) {
		return nil, "", fmt.Errorf("media type not allowed (%s)", contentType)
	}

	return buf.Bytes(), contentType, nil
}

// sniffMimeType uses Go's standard MIME detection to sniff actual content,
// preferring detected type over declared header value.
func sniffMimeType(data []byte, declaredType string) string {
	// Go's net/http.DetectContentType sniffs the first 512 bytes.
	detected := http.DetectContentType(data)

	// Prefer detected type, but respect empty result.
	if detected != "application/octet-stream" {
		return detected
	}
	return declaredType
}

// isSafeContent returns true if the MIME type is safe to return to the browser.
// Unsafe types (HTML, SVG, executables) must be rejected with 415.
func isSafeContent(mimeType string) bool {
	// Normalize MIME type.
	mimeType = strings.ToLower(strings.TrimSpace(mimeType))

	// Remove charset/boundary parameters.
	if idx := strings.Index(mimeType, ";"); idx >= 0 {
		mimeType = mimeType[:idx]
	}
	mimeType = strings.TrimSpace(mimeType)

	// Safe inline content.
	switch mimeType {
	case "image/jpeg", "image/png", "image/webp", "image/gif":
		return true
	}

	// Dangerous content — never return bytes.
	dangerousPrefixes := []string{
		"text/html",
		"image/svg",
		"application/xhtml",
		"application/x-executable",
		"application/x-elf",
		"application/x-mach-binary",
		"application/x-msdownload",
		"application/x-sh",
		"text/x-shellscript",
		"application/x-java-applet",
		"application/x-java-serialized-object",
	}
	for _, dangerous := range dangerousPrefixes {
		if strings.HasPrefix(mimeType, dangerous) {
			return false
		}
	}

	// Unknown MIME type — treat as safe attachment (application/octet-stream).
	// The browser will download, not inline.
	return true
}

// ValidateOriginForTest checks if a URL origin matches the trusted WAHA origin.
// Exposed for testing only.
func (r *MediaRetriever) ValidateOriginForTest(urlStr string) error {
	u, err := url.Parse(urlStr)
	if err != nil {
		return err
	}
	if u.User != nil {
		return fmt.Errorf("userinfo not allowed")
	}
	mediaOrigin := fmt.Sprintf("%s://%s", u.Scheme, u.Host)
	if mediaOrigin != r.trustedWahaOrigin {
		return fmt.Errorf("origin mismatch: %q != %q", mediaOrigin, r.trustedWahaOrigin)
	}
	return nil
}
