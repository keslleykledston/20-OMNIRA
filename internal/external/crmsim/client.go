// Package crmsim is a small REFERENCE CRM for the OMNIRA headless API (ADR-0048). It is written the way a customer's integration would be: only the
// standard library, only the documented HTTP contract (contracts/openapi/omnira-external-v1.yaml), no import of any OMNIRA package. It authenticates with an
// integration token, bootstraps without gaps (checkpoint -> snapshot -> events), receives and verifies signed webhooks, deduplicates by event_id, sends with an
// Idempotency-Key it keeps across retries, and reads 409/412 as "the conversation changed: read it again", never as "retry blindly".
package crmsim

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// Problem is an RFC 9457 document as the API answers it (application/problem+json).
type Problem struct {
	Status int    `json:"status"`
	Code   string `json:"code"`
	Title  string `json:"title"`
	Detail string `json:"detail"`
	// ControlETag is the ETag the refusal carried: the control version the conversation has NOW ("control-N"), when the API said it.
	ControlETag string `json:"-"`
}

func (p *Problem) Error() string { return fmt.Sprintf("%d %s: %s", p.Status, p.Code, p.Detail) }

// IsStale: 412, the control version presented is not the current one. Read the conversation again.
func IsStale(err error) bool {
	var p *Problem
	return errors.As(err, &p) && p.Status == http.StatusPreconditionFailed
}

// IsConflict: 409, someone else holds it / it is closed / outside hours...
func IsConflict(err error) bool {
	var p *Problem
	return errors.As(err, &p) && p.Status == http.StatusConflict
}

// IsGone: 410, the journal no longer has the cursor: rebuild from the snapshot.
func IsGone(err error) bool { var p *Problem; return errors.As(err, &p) && p.Status == http.StatusGone }

// IsNotFound: the uniform 404 (does not exist OR is not visible to this installation).
func IsNotFound(err error) bool {
	var p *Problem
	return errors.As(err, &p) && p.Status == http.StatusNotFound
}

type Holder struct {
	Kind string `json:"kind"`
	Mine bool   `json:"mine"`
}

type Control struct {
	State   string  `json:"state"`
	Holder  *Holder `json:"holder,omitempty"`
	Version int64   `json:"version"`
	Origin  *string `json:"origin,omitempty"`
}

type Conversation struct {
	ID        string  `json:"id"`
	Status    string  `json:"status"`
	ContactID string  `json:"contact_id"`
	QueueID   string  `json:"queue_id"`
	Control   Control `json:"control"`
	Can       struct {
		Claim, Reply, Release, Transfer, Finalize bool
	} `json:"can"`
}

type Message struct {
	ID        string `json:"id"`
	Direction string `json:"direction"`
	Type      string `json:"type"`
	Body      string `json:"body"`
	Status    string `json:"status"`
	Sender    struct {
		Kind string `json:"kind"`
		Mine bool   `json:"mine"`
	} `json:"sender"`
}

type Event struct {
	EventID        string `json:"event_id"`
	Seq            int64  `json:"seq"`
	Type           string `json:"type"`
	ConversationID string `json:"conversation_id"`
	AggregateVer   *int64 `json:"aggregate_version"`
	Origin         struct {
		Kind string `json:"kind"`
		Mine bool   `json:"mine"`
	} `json:"origin"`
	Aggregate struct {
		Type string `json:"type"`
		ID   string `json:"id"`
	} `json:"aggregate"`
	Data map[string]any `json:"data"`
}

type Checkpoint struct {
	SeqHead int64 `json:"seq_head"`
	MinSeq  int64 `json:"min_seq"`
}

// CommandResult is what a write command answers.
type CommandResult struct {
	ConversationID string  `json:"conversation_id"`
	Status         string  `json:"status"`
	Control        Control `json:"control"`
	Changed        *bool   `json:"changed"`
	MessageID      string  `json:"message_id"`
	Delivery       string  `json:"delivery"`
	ClosureID      string  `json:"closure_id"`
	// Replay: the API said this was the stored answer of an earlier identical command (Idempotent-Replay).
	Replay bool `json:"-"`
	// ETag is the control version after the command ("control-N").
	ETag string `json:"-"`
}

type Client struct {
	BaseURL string // https://omnira.example/api/v1/external
	Token   string
	HTTP    *http.Client
	// Retries is how many times a command is repeated, with the SAME Idempotency-Key, after a network failure or a 5xx/429 (the first answer may have been lost).
	Retries int
	Sleep   func(time.Duration)
}

func NewClient(baseURL, token string, h *http.Client) *Client {
	if h == nil {
		h = &http.Client{Timeout: 30 * time.Second}
	}
	return &Client{BaseURL: strings.TrimRight(baseURL, "/"), Token: token, HTTP: h, Retries: 3, Sleep: time.Sleep}
}

// NewKey makes an Idempotency-Key. Keep it with the intent ("send this text to this conversation") and reuse it on every retry of that intent.
func NewKey(prefix string) string {
	b := make([]byte, 12)
	_, _ = rand.Read(b)
	return prefix + "-" + hex.EncodeToString(b)
}

func (c *Client) do(ctx context.Context, method, path string, q url.Values, body any, hdr map[string]string, out any) (http.Header, error) {
	var rdr io.Reader
	if body != nil {
		raw, _ := json.Marshal(body)
		rdr = bytes.NewReader(raw)
	}
	u := c.BaseURL + path
	if len(q) > 0 {
		u += "?" + q.Encode()
	}
	req, err := http.NewRequestWithContext(ctx, method, u, rdr)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+c.Token)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if resp.StatusCode >= 300 {
		p := &Problem{Status: resp.StatusCode, ControlETag: resp.Header.Get("ETag")}
		_ = json.Unmarshal(raw, p)
		if p.Status == 0 {
			p.Status = resp.StatusCode
		}
		return resp.Header, p
	}
	if out != nil {
		if err := json.Unmarshal(raw, out); err != nil {
			return resp.Header, fmt.Errorf("decode %s: %w", path, err)
		}
	}
	return resp.Header, nil
}

// ---- reads ----

func (c *Client) Checkpoint(ctx context.Context) (Checkpoint, error) {
	var cp Checkpoint
	_, err := c.do(ctx, http.MethodGet, "/sync/checkpoint", nil, nil, nil, &cp)
	return cp, err
}

type page[T any] struct {
	Items      []T    `json:"items"`
	NextCursor string `json:"next_cursor"`
	HasMore    bool   `json:"has_more"`
}

// Conversations pages through every conversation the installation sees (status open, closed or all).
func (c *Client) Conversations(ctx context.Context, status string) ([]Conversation, error) {
	var all []Conversation
	cursor := ""
	for {
		q := url.Values{"status": {status}, "limit": {"100"}}
		if cursor != "" {
			q.Set("cursor", cursor)
		}
		var p page[Conversation]
		if _, err := c.do(ctx, http.MethodGet, "/conversations", q, nil, nil, &p); err != nil {
			return nil, err
		}
		all = append(all, p.Items...)
		if !p.HasMore {
			return all, nil
		}
		cursor = p.NextCursor
	}
}

func (c *Client) Conversation(ctx context.Context, id string) (Conversation, error) {
	var v Conversation
	_, err := c.do(ctx, http.MethodGet, "/conversations/"+id, nil, nil, nil, &v)
	return v, err
}

func (c *Client) Messages(ctx context.Context, id string) ([]Message, error) {
	var all []Message
	cursor := ""
	for {
		q := url.Values{"limit": {"200"}}
		if cursor != "" {
			q.Set("cursor", cursor)
		}
		var p page[Message]
		if _, err := c.do(ctx, http.MethodGet, "/conversations/"+id+"/messages", q, nil, nil, &p); err != nil {
			return nil, err
		}
		all = append(all, p.Items...)
		if !p.HasMore {
			return all, nil
		}
		cursor = p.NextCursor
	}
}

// Events returns one page of facts after seq, and the cursor for the next call.
func (c *Client) Events(ctx context.Context, after int64) ([]Event, int64, bool, error) {
	var p struct {
		Items      []Event `json:"items"`
		NextCursor string  `json:"next_cursor"`
		HasMore    bool    `json:"has_more"`
	}
	_, err := c.do(ctx, http.MethodGet, "/events", url.Values{"after": {strconv.FormatInt(after, 10)}, "limit": {"200"}}, nil, nil, &p)
	if err != nil {
		return nil, after, false, err
	}
	next, perr := strconv.ParseInt(p.NextCursor, 10, 64)
	if perr != nil {
		next = after
	}
	return p.Items, next, p.HasMore, nil
}

// ---- commands ----

// Command is one write intent. Key is chosen by the caller ONCE per intent and survives retries.
type Command struct {
	Op      string // claim | release | transfer | messages | finalize
	Conv    string
	Key     string
	Version int64 // the control version read; 0 = send no If-Match (allowed on claim only)
	Body    any
}

func (c *Client) Do(ctx context.Context, cmd Command) (CommandResult, error) {
	hdr := map[string]string{"Idempotency-Key": cmd.Key}
	if cmd.Version > 0 {
		hdr["If-Match"] = `"control-` + strconv.FormatInt(cmd.Version, 10) + `"`
	}
	var last error
	for attempt := 0; attempt <= c.Retries; attempt++ {
		var out CommandResult
		h, err := c.do(ctx, http.MethodPost, "/conversations/"+cmd.Conv+"/"+cmd.Op, nil, cmd.Body, hdr, &out)
		if err == nil {
			out.Replay = h.Get("Idempotent-Replay") == "true"
			out.ETag = h.Get("ETag")
			return out, nil
		}
		last = err
		var p *Problem
		if errors.As(err, &p) && p.Status < 500 && p.Status != http.StatusTooManyRequests && p.Code != "request_in_progress" {
			return out, err // a refusal: 4xx means "do not repeat this"
		}
		if ctx.Err() != nil {
			return out, ctx.Err()
		}
		c.Sleep(time.Duration(attempt+1) * 200 * time.Millisecond) // network failure, 5xx, 429 or "in progress": the SAME key again
	}
	return CommandResult{}, last
}

func (c *Client) Claim(ctx context.Context, conv, key string, version int64) (CommandResult, error) {
	return c.Do(ctx, Command{Op: "claim", Conv: conv, Key: key, Version: version})
}
func (c *Client) Release(ctx context.Context, conv, key string, version int64) (CommandResult, error) {
	return c.Do(ctx, Command{Op: "release", Conv: conv, Key: key, Version: version})
}
func (c *Client) Send(ctx context.Context, conv, key string, version int64, text string) (CommandResult, error) {
	return c.Do(ctx, Command{Op: "messages", Conv: conv, Key: key, Version: version, Body: map[string]any{"text": text}})
}
func (c *Client) SendTemplate(ctx context.Context, conv, key string, version int64, templateID string, params []string) (CommandResult, error) {
	return c.Do(ctx, Command{Op: "messages", Conv: conv, Key: key, Version: version, Body: map[string]any{"template": map[string]any{"id": templateID, "params": params}}})
}
func (c *Client) Transfer(ctx context.Context, conv, key string, version int64, queueID, userID string) (CommandResult, error) {
	body := map[string]any{"queue_id": queueID}
	if userID != "" {
		body["user_id"] = userID
	}
	return c.Do(ctx, Command{Op: "transfer", Conv: conv, Key: key, Version: version, Body: body})
}
func (c *Client) Finalize(ctx context.Context, conv, key string, version int64, reason, note string) (CommandResult, error) {
	return c.Do(ctx, Command{Op: "finalize", Conv: conv, Key: key, Version: version, Body: map[string]any{"reason": reason, "note": note}})
}
