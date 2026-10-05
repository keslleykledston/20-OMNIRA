package application

import (
	"testing"
	"time"
)

func TestSessionWindow(t *testing.T) {
	now := time.Now()
	ago := func(d time.Duration) *time.Time { v := now.Add(-d); return &v }
	cases := []struct {
		name           string
		provider       string
		last           *time.Time
		wantReq, wantO bool
	}{
		{"waha has no window", "waha", nil, false, true},
		{"meta never wrote", ProviderMetaCloud, nil, true, false},
		{"meta within 24h", ProviderMetaCloud, ago(23 * time.Hour), true, true},
		{"meta after 24h", ProviderMetaCloud, ago(25 * time.Hour), true, false},
	}
	for _, c := range cases {
		req, open := SessionWindow(c.provider, c.last, now)
		if req != c.wantReq || open != c.wantO {
			t.Errorf("%s: got required=%v open=%v", c.name, req, open)
		}
	}
}
