package adapters

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"strings"

	"github.com/omnira/omnira/internal/channels/adapters/waha"
	"github.com/omnira/omnira/internal/media/ports"
)

// WahaFetcher downloads a file referenced by a webhook. WAHA reports its own address as the host of the file
// URL (http://localhost:3000/...), which is meaningless from another container, so only the path is trusted:
// it must live under /api/files/ and is re-based onto the configured WAHA origin. The host in the stored
// reference is discarded, which also means a forged reference cannot make OMNIRA call another server.
type WahaFetcher struct {
	client  *waha.Client
	baseURL string
}

var _ ports.Fetcher = (*WahaFetcher)(nil)

func NewWahaFetcher(client *waha.Client, baseURL string) (*WahaFetcher, error) {
	u, err := url.Parse(baseURL)
	if err != nil || u.Scheme == "" || u.Host == "" {
		return nil, fmt.Errorf("media fetcher: invalid WAHA base URL")
	}
	return &WahaFetcher{client: client, baseURL: u.Scheme + "://" + u.Host}, nil
}

// rebase maps a stored media reference onto the trusted origin, or rejects it.
func rebase(baseURL, mediaRef string) (string, error) {
	u, err := url.Parse(mediaRef)
	if err != nil || u.User != nil {
		return "", errors.New("invalid media reference")
	}
	clean := u.Path
	if !strings.HasPrefix(clean, "/api/files/") || strings.Contains(clean, "..") || strings.Contains(clean, "//") {
		return "", errors.New("media reference outside /api/files/")
	}
	return baseURL + clean, nil
}

func (f *WahaFetcher) Fetch(ctx context.Context, mediaRef string) ([]byte, string, error) {
	target, err := rebase(f.baseURL, mediaRef)
	if err != nil {
		return nil, "", fmt.Errorf("%w: %v", ports.ErrSourceGone, err)
	}
	data, mime, err := f.client.DownloadMedia(ctx, target)
	if err != nil {
		if errors.Is(err, waha.ErrPermanent) {
			return nil, "", ports.ErrSourceGone
		}
		return nil, "", err
	}
	return data, mime, nil
}
