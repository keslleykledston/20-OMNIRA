package connectors

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// K3GTicketingConnector talks to K3G's ticket/service-desk namespace
// (/api/support/*), confirmed real by PRODUCT.6-C1 (GET) and PRODUCT.6-H1
// (POST, one authorized controlled production write — ticket.id 28180).
//
// Deliberately a separate type from K3GCRMClient (internal/tool/connectors/
// k3gcrm.go), which only talks to K3G's relationship/activity namespace
// (/api/crm/*) and has no ticket concept at all (see that file's own
// comment). Both may share the same Bearer token/base URL in practice
// (PRODUCT.6-C1 confirmed one token authenticates against both
// namespaces), but that is a deployment fact, not a reason to make one
// type responsible for both domains — CRM/customer and ticketing stay
// separate per ADR-0013/PRODUCT.6-E.
type K3GTicketingConnector struct {
	baseURL string
	token   string
	client  *http.Client
}

// K3GTicketingConfig mirrors K3GCRMConfig's shape deliberately (same
// BaseURL/Token/Timeout fields) — the credential itself is not duplicated
// anywhere; a caller resolves the same stored channel_credentials row
// (PRODUCT.6-C1) and passes its base_url/token fields into whichever
// connector type it is constructing.
type K3GTicketingConfig struct {
	BaseURL string
	Token   string
	Timeout time.Duration
}

func NewK3GTicketingConnector(cfg K3GTicketingConfig) (*K3GTicketingConnector, error) {
	base := strings.TrimRight(strings.TrimSpace(cfg.BaseURL), "/")
	if base == "" || strings.TrimSpace(cfg.Token) == "" {
		return nil, &TicketingError{Code: TicketingUnknownProviderError, Message: "connector not configured: base_url and token are required"}
	}
	if !strings.HasPrefix(base, "https://") && !strings.HasPrefix(base, "http://") {
		return nil, &TicketingError{Code: TicketingUnknownProviderError, Message: "base url must be absolute"}
	}
	timeout := cfg.Timeout
	if timeout <= 0 {
		timeout = 20 * time.Second
	}
	return &K3GTicketingConnector{baseURL: base, token: cfg.Token, client: &http.Client{Timeout: timeout}}, nil
}

func (c *K3GTicketingConnector) Name() string { return "k3g" }

// k3gTicketWire is the subset of K3G's real ticket JSON shape
// (PRODUCT.6-C1/6-H1) this adapter reads. Provider-specific fields
// (crmCompany, legacyGlpiTicketId, acceptanceState, users_id_recipient,
// urgency, ...) intentionally stay here, never crossing into ExternalTicket.
type k3gTicketWire struct {
	ID          json.Number `json:"id"`
	StatusID    json.Number `json:"statusId"`
	Status      string      `json:"status"`
	StatusLabel string      `json:"statusLabel"`
}

type k3gCreateResponse struct {
	OK     bool          `json:"ok"`
	Ticket k3gTicketWire `json:"ticket"`
	Source string        `json:"source"`
	Error  string        `json:"error"`
	Code   string        `json:"code"`
}

type k3gGetResponse struct {
	Ticket k3gTicketWire `json:"ticket"`
	Error  string        `json:"error"`
	Code   string        `json:"code"`
}

func (w k3gTicketWire) toExternalTicket() (*ExternalTicket, error) {
	id := strings.TrimSpace(w.ID.String())
	if id == "" || id == "0" {
		return nil, &TicketingError{Code: TicketingUnknownProviderError, Message: "provider response is missing a usable ticket id"}
	}
	status := strings.TrimSpace(w.Status)
	if status == "" {
		status = strings.TrimSpace(w.StatusID.String())
	}
	return &ExternalTicket{
		ExternalID:          id,
		ExternalStatus:      status,
		ExternalStatusLabel: strings.TrimSpace(w.StatusLabel),
	}, nil
}

// CreateTicket sends exactly the V1-validated minimal payload
// (companyId/name/content) and performs exactly one HTTP attempt — no
// retry on timeout, EOF, 429 or 5xx. See TicketingConnector.CreateTicket's
// doc comment for why: K3G has no idempotency mechanism, so a second
// attempt after an ambiguous failure could silently duplicate the ticket.
func (c *K3GTicketingConnector) CreateTicket(ctx context.Context, req CreateTicketRequest) (*ExternalTicket, error) {
	payload := map[string]string{
		"companyId": req.CustomerExternalID,
		"name":      req.Subject,
		"content":   req.Description,
	}
	body, err := c.doOnce(ctx, http.MethodPost, "/api/support/tickets", payload)
	if err != nil {
		return nil, err
	}
	var resp k3gCreateResponse
	if err := json.Unmarshal(body, &resp); err != nil {
		return nil, &TicketingError{Code: TicketingUnknownProviderError, Message: "malformed create response", Err: err}
	}
	if !resp.OK {
		return nil, &TicketingError{Code: TicketingUnknownProviderError, Message: "provider returned ok=false for a 2xx create response"}
	}
	return resp.Ticket.toExternalTicket()
}

// GetTicket is read-only and equally single-attempt (no retry logic exists
// in this adapter for any method — CreateTicket's no-retry rule is a
// property of the underlying transport, not a special case).
func (c *K3GTicketingConnector) GetTicket(ctx context.Context, externalTicketID string) (*ExternalTicket, error) {
	if strings.TrimSpace(externalTicketID) == "" {
		return nil, &TicketingError{Code: TicketingValidationError, Message: "externalTicketID is required"}
	}
	body, err := c.doOnce(ctx, http.MethodGet, "/api/support/tickets/"+externalTicketID, nil)
	if err != nil {
		return nil, err
	}
	var resp k3gGetResponse
	if err := json.Unmarshal(body, &resp); err != nil {
		return nil, &TicketingError{Code: TicketingUnknownProviderError, Message: "malformed get response", Err: err}
	}
	return resp.Ticket.toExternalTicket()
}

// doOnce issues exactly one HTTP request and classifies the outcome into a
// *TicketingError on any non-2xx result or transport failure. "Once" is
// structural: there is no loop, no backoff, nothing here that could ever
// send a second request for a single doOnce call.
func (c *K3GTicketingConnector) doOnce(ctx context.Context, method, path string, jsonBody any) ([]byte, error) {
	var reader io.Reader
	if jsonBody != nil {
		b, err := json.Marshal(jsonBody)
		if err != nil {
			return nil, &TicketingError{Code: TicketingUnknownProviderError, Message: "failed to encode request body", Err: err}
		}
		reader = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.baseURL+path, reader)
	if err != nil {
		return nil, &TicketingError{Code: TicketingUnknownProviderError, Message: "failed to build request", Err: err}
	}
	req.Header.Set("Authorization", "Bearer "+c.token)
	req.Header.Set("Accept", "application/json")
	if jsonBody != nil {
		req.Header.Set("Content-Type", "application/json")
	}

	res, err := c.client.Do(req)
	if err != nil {
		// Network timeout, connection reset, DNS failure, etc. — never
		// retried by this method; the caller decides what happens next.
		return nil, &TicketingError{Code: TicketingProviderUnavailable, Message: "transport error", Err: err}
	}
	defer res.Body.Close()
	body, readErr := io.ReadAll(io.LimitReader(res.Body, 1<<20))
	if readErr != nil {
		return nil, &TicketingError{Code: TicketingUnknownProviderError, Message: "failed to read response body", Err: readErr}
	}

	if res.StatusCode >= 200 && res.StatusCode < 300 {
		return body, nil
	}
	return nil, classifyK3GError(res.StatusCode, body)
}

// classifyK3GError maps an HTTP failure into the provider-neutral
// TicketingErrorCode model (PRODUCT.6-E), using K3G's stable "code" field
// (PRODUCT.6-C1 confirmed {"error":..., "code":"CRM_TICKET_NOT_MIGRATED"})
// in preference to matching on the human-readable Portuguese message.
func classifyK3GError(status int, body []byte) *TicketingError {
	var parsed struct {
		Error string `json:"error"`
		Code  string `json:"code"`
	}
	_ = json.Unmarshal(body, &parsed) // best-effort; absence doesn't change classification below

	switch {
	case status == http.StatusUnauthorized || status == http.StatusForbidden:
		return &TicketingError{Code: TicketingUnauthorized, Message: "credential rejected", Err: bodyErr(status, parsed.Error)}
	case status == http.StatusBadRequest:
		return &TicketingError{Code: TicketingValidationError, Message: "request rejected as invalid", Err: bodyErr(status, parsed.Error)}
	case status == http.StatusNotFound && parsed.Code == "CRM_TICKET_NOT_MIGRATED":
		return &TicketingError{Code: TicketingNotMigrated, Message: "ticket not yet available in CRM-native mode", Err: bodyErr(status, parsed.Error)}
	case status == http.StatusNotFound:
		return &TicketingError{Code: TicketingNotFound, Message: "ticket not found", Err: bodyErr(status, parsed.Error)}
	case status == http.StatusTooManyRequests:
		return &TicketingError{Code: TicketingProviderUnavailable, Message: "rate limited", Err: bodyErr(status, parsed.Error)}
	case status >= 500:
		return &TicketingError{Code: TicketingProviderUnavailable, Message: "provider server error", Err: bodyErr(status, parsed.Error)}
	default:
		return &TicketingError{Code: TicketingUnknownProviderError, Message: "unclassified provider response", Err: bodyErr(status, parsed.Error)}
	}
}

func bodyErr(status int, msg string) error {
	if msg == "" {
		return errors.New("status " + strconv.Itoa(status))
	}
	return fmt.Errorf("status %d: %s", status, msg)
}
