package adapters

import (
	"context"
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
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if h.att == nil {
			http.Error(w, "outbound media is not enabled", http.StatusNotImplemented)
			return
		}
		principal, err := authn.FromContext(r.Context())
		if err != nil {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		if !h.limiter.allow(principal.UserID) {
			http.Error(w, "too many uploads, wait a moment", http.StatusTooManyRequests)
			return
		}
		select {
		case h.slots <- struct{}{}:
			defer func() { <-h.slots }()
		default:
			w.Header().Set("Retry-After", "3")
			http.Error(w, "the server is busy with other uploads, try again in a moment", http.StatusServiceUnavailable)
			return
		}
		rc := http.NewResponseController(w)
		_ = rc.SetReadDeadline(time.Now().Add(uploadReadTimeout))
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
