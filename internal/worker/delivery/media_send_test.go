package delivery_test

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"testing"

	"github.com/google/uuid"
	"github.com/omnira/omnira/internal/channels/ports"
	"github.com/omnira/omnira/internal/worker/delivery"
)

type fakeFiles struct {
	data  []byte
	err   error
	asked struct {
		tenant, id uuid.UUID
		size       int64
		sha        string
	}
}

func (f *fakeFiles) ReadVerified(tenant, id uuid.UUID, size int64, sha string) ([]byte, error) {
	f.asked.tenant, f.asked.id, f.asked.size, f.asked.sha = tenant, id, size, sha
	return f.data, f.err
}

func mediaSetup(t *testing.T) (*delivery.Handler, *fakeStore, *fakeSender, *fakeFiles, []byte) {
	h, store, sender, raw := setup(t)
	payload := []byte("%PDF-1.4 hello")
	sum := sha256.Sum256(payload)
	store.job.Text = "segue o boleto"
	store.job.Media = &delivery.MediaJob{AttachmentID: uuid.New(), TenantID: uuid.New(), Kind: "document", Mime: "application/pdf", FileName: "boleto.pdf",
		Size: int64(len(payload)), SHA256: hex.EncodeToString(sum[:])}
	files := &fakeFiles{data: payload}
	return h.WithMediaFiles(files), store, sender, files, raw
}

func TestMediaMessageIsDeliveredWithVerifiedBytesAndTheCaption(t *testing.T) {
	h, store, sender, files, raw := mediaSetup(t)
	if err := h.Handle(context.Background(), raw, 1); err != nil {
		t.Fatal(err)
	}
	if store.job.Status != "sent" || store.sentID == "" {
		t.Fatalf("status=%s sentID=%q", store.job.Status, store.sentID)
	}
	m := sender.mediaGot
	if m == nil || string(m.Data) != "%PDF-1.4 hello" || m.Caption != "segue o boleto" || m.FileName != "boleto.pdf" || m.Mime != "application/pdf" || m.Kind != "document" || m.ToE164 != "+5511999990000" {
		t.Fatalf("provider got %+v", m)
	}
	if files.asked.id != store.job.Media.AttachmentID || files.asked.tenant != store.job.Media.TenantID || files.asked.sha != store.job.Media.SHA256 || files.asked.size != store.job.Media.Size {
		t.Fatalf("the file was read without the recorded size/hash: %+v", files.asked)
	}
	if sender.newIDCalls != 0 {
		t.Fatal("a media send must not reserve a provider id (no provider deduplicates media by id)")
	}
}

func TestMediaThatNoLongerMatchesItsRecordIsNeverSent(t *testing.T) {
	for name, files := range map[string]*fakeFiles{"altered or missing file": {err: errors.New("mismatch")}} {
		h, store, sender, _, raw := mediaSetup(t)
		h.WithMediaFiles(files)
		if err := h.Handle(context.Background(), raw, 1); err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if store.job.Status != "failed" || store.failure != "media_unavailable" || sender.calls != 0 {
			t.Fatalf("%s: status=%s failure=%q provider calls=%d", name, store.job.Status, store.failure, sender.calls)
		}
	}
	// A worker without the media store cannot deliver files and says so instead of sending text.
	h, store, sender, _, raw := mediaSetup(t)
	h.WithMediaFiles(nil)
	if err := h.Handle(context.Background(), raw, 1); err != nil || store.job.Status != "failed" || sender.calls != 0 {
		t.Fatalf("no store: err=%v status=%s calls=%d", err, store.job.Status, sender.calls)
	}
}

func TestAnAmbiguousMediaFailureEndsAsUncertainAndIsNeverRetried(t *testing.T) {
	h, store, sender, _, raw := mediaSetup(t)
	sender.mediaErr = ports.ErrOutcomeUnknown
	if err := h.Handle(context.Background(), raw, 1); err != nil {
		t.Fatal(err)
	}
	if store.job.Status != "uncertain" || sender.calls != 1 {
		t.Fatalf("status=%s calls=%d", store.job.Status, sender.calls)
	}
}

func TestAThrottledMediaSendIsRetriedAndARejectionFails(t *testing.T) {
	h, store, sender, _, raw := mediaSetup(t)
	sender.mediaErr = ports.ErrRateLimited
	if err := h.Handle(context.Background(), raw, 1); err == nil || store.job.Status != "queued" {
		t.Fatalf("a throttle must ask for a redelivery: err=%v status=%s", err, store.job.Status)
	}
	h, store, sender, _, raw = mediaSetup(t)
	sender.mediaErr = ports.ErrPermanent
	if err := h.Handle(context.Background(), raw, 1); err != nil || store.job.Status != "failed" {
		t.Fatalf("a rejection must fail the message: err=%v status=%s", err, store.job.Status)
	}
}
