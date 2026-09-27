package jobsstream

import (
	"context"
	"testing"
	"time"

	"github.com/nats-io/nats.go/jetstream"
)

// TestVerifyConfig_AcceptsExactCanonicalPolicy proves the canonical Config()
// verifies against itself — the baseline every mismatch case below is
// contrasted against.
func TestVerifyConfig_AcceptsExactCanonicalPolicy(t *testing.T) {
	cfg := Config()
	if err := verifyConfig(cfg, cfg); err != nil {
		t.Fatalf("verifyConfig(cfg, cfg) = %v, want nil", err)
	}
}

// TestVerifyConfig_RejectsMismatch is the deterministic, network-free proof
// that Ensure's own verification step (verifyConfig) rejects a server-
// returned config that doesn't match the canonical policy — one sub-test per
// governed field, so a regression that silently drops a field from
// verifyConfig is caught immediately.
func TestVerifyConfig_RejectsMismatch(t *testing.T) {
	base := Config()

	t.Run("max_age mismatch", func(t *testing.T) {
		got := base
		got.MaxAge = base.MaxAge + time.Hour
		if err := verifyConfig(base, got); err == nil {
			t.Fatal("verifyConfig accepted a wrong MaxAge, want error")
		}
	})
	t.Run("max_bytes mismatch", func(t *testing.T) {
		got := base
		got.MaxBytes = base.MaxBytes + 1
		if err := verifyConfig(base, got); err == nil {
			t.Fatal("verifyConfig accepted a wrong MaxBytes, want error")
		}
	})
	t.Run("max_msgs mismatch", func(t *testing.T) {
		got := base
		got.MaxMsgs = 100
		if err := verifyConfig(base, got); err == nil {
			t.Fatal("verifyConfig accepted a wrong MaxMsgs, want error")
		}
	})
	t.Run("discard mismatch", func(t *testing.T) {
		got := base
		got.Discard = jetstream.DiscardOld
		if err := verifyConfig(base, got); err == nil {
			t.Fatal("verifyConfig accepted a wrong Discard policy, want error")
		}
	})
	t.Run("retention mismatch", func(t *testing.T) {
		got := base
		got.Retention = jetstream.InterestPolicy
		if err := verifyConfig(base, got); err == nil {
			t.Fatal("verifyConfig accepted a wrong Retention policy, want error")
		}
	})
	t.Run("storage mismatch", func(t *testing.T) {
		got := base
		got.Storage = jetstream.MemoryStorage
		if err := verifyConfig(base, got); err == nil {
			t.Fatal("verifyConfig accepted a wrong Storage type, want error")
		}
	})
	t.Run("duplicates mismatch", func(t *testing.T) {
		got := base
		got.Duplicates = base.Duplicates + time.Second
		if err := verifyConfig(base, got); err == nil {
			t.Fatal("verifyConfig accepted a wrong Duplicates window, want error")
		}
	})
	t.Run("subjects mismatch", func(t *testing.T) {
		got := base
		got.Subjects = []string{"other.>"}
		if err := verifyConfig(base, got); err == nil {
			t.Fatal("verifyConfig accepted wrong Subjects, want error")
		}
	})
	t.Run("name mismatch", func(t *testing.T) {
		got := base
		got.Name = "SOMETHING_ELSE"
		if err := verifyConfig(base, got); err == nil {
			t.Fatal("verifyConfig accepted a wrong Name, want error")
		}
	})
}

// TestEnsureWithConfig_PropagatesVerificationFailure proves Ensure's actual
// call chain — not just verifyConfig in isolation — surfaces a verification
// failure as an error without panicking, using a fake JetStream that
// deliberately returns a stream whose Info() disagrees with the config it
// was asked to apply. No real NATS connection involved (this is the
// "deterministic, no live-NATS-mutation" half of the requirement; the
// disposable-NATS half — proving the real server round-trip — is
// TestEnsure_ExistingStreamUpdate_* in ensure_disposable_test.go).
func TestEnsureWithConfig_PropagatesVerificationFailure(t *testing.T) {
	cfg := Config()
	cfg.Name = "TEST_MISMATCH_STREAM"
	badReturn := cfg
	badReturn.MaxAge = cfg.MaxAge + time.Hour // server "accepted" something else

	js := &fakeStreamManager{info: &jetstream.StreamInfo{Config: badReturn}}
	if _, err := ensureWithConfig(context.Background(), js, cfg); err == nil {
		t.Fatal("ensureWithConfig accepted a mismatched read-back config, want error")
	}
	if js.createOrUpdateCalls != 1 {
		t.Fatalf("CreateOrUpdateStream called %d time(s), want exactly 1", js.createOrUpdateCalls)
	}
}

// fakeStreamManager and fakeStream implement just enough of
// jetstream.JetStream/jetstream.Stream (via the streamManager interface) to
// drive ensureWithConfig without any real NATS connection.
type fakeStreamManager struct {
	info                *jetstream.StreamInfo
	err                 error
	createOrUpdateCalls int
}

func (f *fakeStreamManager) CreateOrUpdateStream(ctx context.Context, cfg jetstream.StreamConfig) (jetstream.Stream, error) {
	f.createOrUpdateCalls++
	if f.err != nil {
		return nil, f.err
	}
	return &fakeStream{info: f.info}, nil
}

func (f *fakeStreamManager) Stream(ctx context.Context, name string) (jetstream.Stream, error) {
	return &fakeStream{info: f.info}, nil
}

// fakeStream embeds the (nil) jetstream.Stream interface so it satisfies the
// full interface without implementing every method — only Info is ever
// called by ensureWithConfig.
type fakeStream struct {
	jetstream.Stream
	info *jetstream.StreamInfo
}

func (f *fakeStream) Info(ctx context.Context, opts ...jetstream.StreamInfoOpt) (*jetstream.StreamInfo, error) {
	return f.info, nil
}
