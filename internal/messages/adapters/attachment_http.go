package adapters

import (
	"encoding/json"
	"errors"
	"io"
	"mime/multipart"
	"net/http"
	"sync"
	"time"

	"github.com/google/uuid"
	mediadomain "github.com/omnira/omnira/internal/media/domain"
	"github.com/omnira/omnira/internal/messages/application"
	tenancydomain "github.com/omnira/omnira/internal/tenancy/domain"
)

type attachmentResponse struct {
	ID        uuid.UUID `json:"id"`
	Kind      string    `json:"kind"`
	Mime      string    `json:"mime"`
	SizeBytes int64     `json:"size_bytes"`
	FileName  string    `json:"file_name"`
	ExpiresAt string    `json:"expires_at"`
}

// uploadLimiter caps uploads per operator (a fixed window, in memory): a runaway client cannot fill the disk or keep the antivirus busy.
type uploadLimiter struct {
	mu     sync.Mutex
	max    int
	window time.Duration
	hits   map[uuid.UUID][]time.Time
}

func newUploadLimiter(max int, window time.Duration) *uploadLimiter {
	return &uploadLimiter{max: max, window: window, hits: map[uuid.UUID][]time.Time{}}
}

func (l *uploadLimiter) allow(user uuid.UUID) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := time.Now()
	kept := l.hits[user][:0]
	for _, t := range l.hits[user] {
		if now.Sub(t) < l.window {
			kept = append(kept, t)
		}
	}
	if len(kept) >= l.max {
		l.hits[user] = kept
		return false
	}
	l.hits[user] = append(kept, now)
	return true
}

// Upload serves POST /inbox/conversations/{id}/attachments (multipart/form-data, one part named "file"). The body is STREAMED with a hard
// ceiling and never spooled to a temporary file; only the bytes of that one part are kept, in memory, until they are validated.
func (h *SendHandler) Upload(w http.ResponseWriter, r *http.Request) {
	if h.att == nil {
		http.Error(w, "outbound media is not enabled", http.StatusNotImplemented)
		return
	}
	conversationID, err := uuid.Parse(r.PathValue("conversation_id"))
	if err != nil {
		http.Error(w, "invalid conversation_id", http.StatusBadRequest)
		return
	}
	tc, err := tenancydomain.FromContext(r.Context())
	if err != nil || tc.ActorID == uuid.Nil {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	if !h.limiter.allow(tc.ActorID) {
		http.Error(w, "too many uploads, wait a moment", http.StatusTooManyRequests)
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, mediadomain.MaxOutboundBytes+(1<<20))
	mr, err := r.MultipartReader()
	if err != nil {
		http.Error(w, "multipart/form-data with a \"file\" part is required", http.StatusBadRequest)
		return
	}
	var part *multipart.Part
	for {
		p, err := mr.NextPart()
		if err == io.EOF {
			break
		}
		if err != nil {
			http.Error(w, "invalid or oversized upload", http.StatusRequestEntityTooLarge)
			return
		}
		if p.FormName() == "file" {
			part = p
			break
		}
		_ = p.Close()
	}
	if part == nil {
		http.Error(w, "a \"file\" part is required", http.StatusBadRequest)
		return
	}
	data, err := io.ReadAll(io.LimitReader(part, mediadomain.MaxOutboundBytes+1))
	if err != nil {
		http.Error(w, "invalid or oversized upload", http.StatusRequestEntityTooLarge)
		return
	}
	if len(data) > mediadomain.MaxOutboundBytes {
		http.Error(w, "file too large (limit 16 MiB)", http.StatusRequestEntityTooLarge)
		return
	}
	att, err := h.att.Upload(r.Context(), conversationID, part.FileName(), part.Header.Get("Content-Type"), data)
	if err != nil {
		if rej := (*application.AttachmentRejected)(nil); errors.As(err, &rej) && rej.Reason == mediadomain.ReasonTooLarge {
			http.Error(w, "file too large (limit 16 MiB)", http.StatusRequestEntityTooLarge)
			return
		}
		fail(w, err)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusCreated)
	_ = json.NewEncoder(w).Encode(attachmentResponse{
		ID: att.ID, Kind: att.Kind, Mime: att.Mime, SizeBytes: att.SizeBytes, FileName: att.FileName, ExpiresAt: att.ExpiresAt.UTC().Format(time.RFC3339),
	})
}

// RemoveAttachment serves DELETE /inbox/conversations/{id}/attachments/{attachment_id}: the operator took the file out of the composer.
func (h *SendHandler) RemoveAttachment(w http.ResponseWriter, r *http.Request) {
	if h.att == nil {
		http.Error(w, "outbound media is not enabled", http.StatusNotImplemented)
		return
	}
	conversationID, err := uuid.Parse(r.PathValue("conversation_id"))
	if err != nil {
		http.Error(w, "invalid conversation_id", http.StatusBadRequest)
		return
	}
	attachmentID, err := uuid.Parse(r.PathValue("attachment_id"))
	if err != nil {
		http.Error(w, "invalid attachment_id", http.StatusBadRequest)
		return
	}
	if err := h.att.Remove(r.Context(), conversationID, attachmentID); err != nil {
		fail(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
