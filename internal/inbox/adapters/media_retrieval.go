package adapters

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	platformdb "github.com/omnira/omnira/internal/platform/db"
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
		// TEST.2/INBOX.MEDIA.1: was the bare int 30 — time.Duration is
		// nanoseconds, so that meant ~30ns, not 30s. Every real WAHA fetch
		// timed out instantly; masked until TEST.2 gave the media-type
		// tests a real message row to reach this call at all.
		Timeout: 30 * time.Second,
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

	// Classify content from actual bytes — never trust the provider's
	// declared Content-Type header (INBOX.MEDIA.2).
	contentType, err := classifyMedia(buf.Bytes())
	if err != nil {
		return nil, "", err
	}

	return buf.Bytes(), contentType, nil
}

// classifyMedia decides the MIME type OMNIRA will report for retrieved
// bytes, and rejects active content outright (returns an error, no bytes).
//
// INBOX.MEDIA.2: this replaces a version that sniffed with
// http.DetectContentType and asked isSafeContent whether the RESULT looked
// dangerous. That order is unsafe for SVG specifically: Go's stdlib sniffer
// (net/http.DetectContentType) has no signature for SVG at all — a real
// `<svg>...<script>...</script></svg>` payload sniffs as plain
// "text/plain; charset=utf-8", which is neither in the safe raster list nor
// in any dangerous-prefix list, so the old code fell through to its "unknown
// type, treat as safe attachment" default and let it through. Checking for
// SVG explicitly, before consulting DetectContentType at all, closes that
// gap without needing a real XML parser.
//
// Order matters and is deliberate:
//  1. Explicit SVG lexical check (isLikelySVG) — independent of whatever
//     DetectContentType would have guessed.
//  2. DetectContentType's own dangerous-prefix matches (HTML, executables,
//     etc.) — kept for defense in depth even where its signature table
//     does cover the format.
//  3. Known-safe raster signatures → that exact MIME type, inline-eligible.
//  4. Everything else (including a weak/ambiguous sniff like "text/plain"
//     for a handful of unrecognized bytes, or the provider's own declared
//     header) is never trusted as authoritative: always normalized to
//     "application/octet-stream", never inline-eligible
//     (internal/inbox/adapters/http.go's isInlineImage only allows the
//     four raster MIME types from step 3, so this is enforced twice).
func classifyMedia(data []byte) (string, error) {
	if isLikelySVG(data) {
		return "", errors.New("media type not allowed (image/svg+xml)")
	}

	detected := http.DetectContentType(data)
	normalized := strings.ToLower(strings.TrimSpace(strings.SplitN(detected, ";", 2)[0]))

	if isDangerousMime(normalized) {
		return "", fmt.Errorf("media type not allowed (%s)", normalized)
	}

	switch normalized {
	case "image/jpeg", "image/png", "image/webp", "image/gif":
		return normalized, nil
	}

	return "application/octet-stream", nil
}

// isDangerousMime reports whether a sniffed MIME type is known active
// content that must never be returned. "image/svg" is kept here as defense
// in depth even though DetectContentType never actually produces it today —
// classifyMedia's isLikelySVG check is what actually catches real SVG.
func isDangerousMime(mimeType string) bool {
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
			return true
		}
	}
	return false
}

// isLikelySVG performs bounded lexical detection of an SVG root element —
// not a real XML parser: no entity expansion, no DTD/external-resource
// resolution, no recursion, and inspection is capped to the first 4096
// bytes. It tolerates the prefixes real SVG files commonly have before the
// root element (UTF-8 BOM, leading whitespace, an XML declaration, a
// DOCTYPE, leading comments) so it isn't fooled by trivial reordering, but
// it deliberately does not try to be a general-purpose XML sniffer.
func isLikelySVG(data []byte) bool {
	b := data
	if len(b) > 4096 {
		b = b[:4096]
	}
	b = bytes.TrimPrefix(b, []byte{0xEF, 0xBB, 0xBF}) // UTF-8 BOM
	b = bytes.TrimLeft(b, " \t\r\n")

	if bytes.HasPrefix(b, []byte("<?xml")) {
		if idx := bytes.Index(b, []byte("?>")); idx >= 0 {
			b = bytes.TrimLeft(b[idx+2:], " \t\r\n")
		}
	}
	if len(b) >= 9 && strings.EqualFold(string(b[:9]), "<!doctype") {
		if idx := bytes.IndexByte(b, '>'); idx >= 0 {
			b = bytes.TrimLeft(b[idx+1:], " \t\r\n")
		}
	}
	for bytes.HasPrefix(b, []byte("<!--")) {
		idx := bytes.Index(b, []byte("-->"))
		if idx < 0 {
			break
		}
		b = bytes.TrimLeft(b[idx+3:], " \t\r\n")
	}

	return len(b) >= 4 && strings.EqualFold(string(b[:4]), "<svg")
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
