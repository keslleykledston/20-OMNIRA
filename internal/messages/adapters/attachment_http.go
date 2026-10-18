package adapters

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log"
	"mime/multipart"
	"net/http"
	"sync"
	"time"

	"github.com/google/uuid"
	mediadomain "github.com/omnira/omnira/internal/media/domain"
	"github.com/omnira/omnira/internal/messages/application"
	"github.com/omnira/omnira/internal/platform/authn"
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

type uploadedFile struct {
	name, mime string
	data       []byte
}

type uploadedFileKey struct{}

// BufferUpload reads the multipart body OUTSIDE the tenant session. The session middleware holds a database connection for as long as the
// handler runs, so reading a slow client's body inside it would let a few slow uploads exhaust the connection pool. Here a slow client
// costs only one of the few upload slots (and a read deadline); the database is touched afterwards, in milliseconds.
// Mount it after authentication and before the tenant session. Only the bytes of the part named "file" are kept (limit 16 MiB), in memory,
// never in a temporary file.
func (h *SendHandler) BufferUpload(next http.Handler) http.Handler {
	return bufferUpload(next, func() bool { return h.att != nil }, func() *uploadLimiter { return h.limiter }, func() chan struct{} { return h.slots }, func(r *http.Request) (uuid.UUID, bool) {
		principal, err := authn.FromContext(r.Context())
		return principal.UserID, err == nil
	})
}

// UploadBuffer is BufferUpload for a caller that is not a signed-in person (an external application, ADR-0048): ownerOf names who the rate limit counts, the way
// the person's id does for an operator. It has its own limiter and its own upload slots, so a busy integration cannot starve the operators' composer (nor the reverse).
func UploadBuffer(enabled func() bool, ownerOf func(*http.Request) (uuid.UUID, bool)) func(http.Handler) http.Handler {
	limiter, slots := newUploadLimiter(20, time.Minute), make(chan struct{}, maxConcurrentUploads)
	return func(next http.Handler) http.Handler {
		return bufferUpload(next, enabled, func() *uploadLimiter { return limiter }, func() chan struct{} { return slots }, ownerOf)
	}
}

func bufferUpload(next http.Handler, enabled func() bool, limiterOf func() *uploadLimiter, slotsOf func() chan struct{}, ownerOf func(*http.Request) (uuid.UUID, bool)) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !enabled() {
			http.Error(w, "outbound media is not enabled", http.StatusNotImplemented)
			return
		}
		owner, ok := ownerOf(r)
		if !ok {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		if !limiterOf().allow(owner) {
			http.Error(w, "too many uploads, wait a moment", http.StatusTooManyRequests)
			return
		}
		slots := slotsOf() // captured: SetUploadSlots must not change the channel this request releases
		select {
		case slots <- struct{}{}:
			defer func() { <-slots }()
		default:
			w.Header().Set("Retry-After", "3")
			http.Error(w, "the server is busy with other uploads, try again in a moment", http.StatusServiceUnavailable)
			return
		}
		rc := http.NewResponseController(w)
		if err := rc.SetReadDeadline(time.Now().Add(uploadReadTimeout)); err != nil {
			log.Printf("messages: upload read deadline not applied (the server's own read timeout governs): %v", err)
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
		_ = rc.SetReadDeadline(time.Time{})
		f := uploadedFile{name: part.FileName(), mime: part.Header.Get("Content-Type"), data: data}
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), uploadedFileKey{}, f)))
	})
}

// UploadedFileFromContext returns the file BufferUpload read for this request (name and content type as the client declared them, the bytes already size-limited).
// Other write paths (the Hub's, for an agent) mount BufferUpload in front of their own handler and read the file through here.
func UploadedFileFromContext(ctx context.Context) (name, mime string, data []byte, ok bool) {
	f, found := ctx.Value(uploadedFileKey{}).(uploadedFile)
	if !found {
		return "", "", nil, false
	}
	return f.name, f.mime, f.data, true
}

const uploadReadTimeout = 60 * time.Second

// Upload serves POST /inbox/conversations/{id}/attachments for a body already read by BufferUpload.
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
	f, ok := r.Context().Value(uploadedFileKey{}).(uploadedFile)
	if !ok {
		http.Error(w, "a \"file\" part is required", http.StatusBadRequest)
		return
	}
	att, err := h.att.Upload(r.Context(), conversationID, f.name, f.mime, f.data)
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
