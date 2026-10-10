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

// k3gMutationResponse is the shared shape of every K3G mutating ticket
// response confirmed real so far: CreateTicket (PRODUCT.6-H1, ticket
// 28180) and UpdateTicketStatus (PRODUCT.6-O2A3, ticket 9115) both return
// exactly {ok, ticket, source, ...} on success.
type k3gMutationResponse struct {
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
	// mutating=true: PRODUCT.6-K1 — a transport failure or 5xx/429 here
	// leaves the write outcome unknown, never "safe to consider
	// not-written" (TicketingProviderUnavailable), because K3G gives no
	// way to tell whether the POST reached the server and committed.
	body, err := c.doOnce(ctx, http.MethodPost, "/api/support/tickets", payload, true)
	if err != nil {
		return nil, err
	}
	var resp k3gMutationResponse
	if err := json.Unmarshal(body, &resp); err != nil {
		// A 2xx response that fails to decode is exactly the ambiguous
		// case PRODUCT.6-K1 calls out: the provider returned success but
		// OMNIRA cannot read the ticket identity out of it.
		return nil, &TicketingError{Code: TicketingWriteOutcomeUnknown, Message: "malformed create response on a successful status", Err: err}
	}
	if !resp.OK {
		return nil, &TicketingError{Code: TicketingWriteOutcomeUnknown, Message: "provider returned ok=false for a 2xx create response"}
	}
	ticket, err := resp.Ticket.toExternalTicket()
	if err != nil {
		// 2xx with no usable ticket.id: the provider claimed success but
		// gave no identity to record — cannot be trusted as a definitive
		// success, and must not be silently ignored as a failure either.
		return nil, &TicketingError{Code: TicketingWriteOutcomeUnknown, Message: "successful create response is missing a usable ticket id", Err: err}
	}
	return ticket, nil
}

// GetTicket is read-only and equally single-attempt (no retry logic exists
// in this adapter for any method — CreateTicket's no-retry rule is a
// property of the underlying transport, not a special case).
func (c *K3GTicketingConnector) GetTicket(ctx context.Context, externalTicketID string) (*ExternalTicket, error) {
	if strings.TrimSpace(externalTicketID) == "" {
		return nil, &TicketingError{Code: TicketingValidationError, Message: "externalTicketID is required"}
	}
	body, err := c.doOnce(ctx, http.MethodGet, "/api/support/tickets/"+externalTicketID, nil, false)
	if err != nil {
		return nil, err
	}
	var resp k3gGetResponse
	if err := json.Unmarshal(body, &resp); err != nil {
		return nil, &TicketingError{Code: TicketingUnknownProviderError, Message: "malformed get response", Err: err}
	}
	return resp.Ticket.toExternalTicket()
}

// k3gStatusCodes is the official K3G ticket status vocabulary confirmed by
// the K3G team (PRODUCT.6-O2A2) and exercised live for codes 1 (create,
// ticket 28180) and 5 (status mutation, ticket 9115, PRODUCT.6-O2A3). 2/3/
// 4/6 are documented but not yet live-mutated — they are accepted here
// because the OFFICIAL vocabulary, not live-mutation history, is what
// bounds which requests this adapter will ever send to the provider.
var k3gStatusCodes = map[string]int{"1": 1, "2": 2, "3": 3, "4": 4, "5": 5, "6": 6}

// k3gStatusWireValue validates target.Code against the official K3G
// vocabulary BEFORE any network call — an empty string, "0", "7", a
// lifecycle-intent word ("resolved"/"closed"), a zero-padded variant
// ("05"), or any whitespace-padded variant (" 1", "1 ") are all rejected
// here, never forwarded to the provider. Deliberately an EXACT map lookup,
// no trimming/normalization: a code this strict-by-construction type
// doesn't accidentally accept is safer than one that silently tolerates
// near-misses from a caller.
func k3gStatusWireValue(target ExternalStatusTarget) (int, error) {
	v, ok := k3gStatusCodes[target.Code]
	if !ok {
		return 0, &TicketingError{Code: TicketingValidationError, Message: fmt.Sprintf("unsupported external status code %q", target.Code)}
	}
	return v, nil
}

// UpdateTicketStatus implements TicketingConnector.UpdateTicketStatus
// (PRODUCT.6-O2B2) against the confirmed real K3G contract
// (PRODUCT.6-O2A3): PUT /api/support/tickets/{id}/status, body
// {"status": <int>}. Exactly one HTTP attempt — no retry, no pre-GET (a
// same-target resubmission is provider-confirmed safe, but that is
// application-level orchestration's concern, PRODUCT.6-O2B3, never this
// adapter's).
func (c *K3GTicketingConnector) UpdateTicketStatus(ctx context.Context, externalID string, target ExternalStatusTarget) (*ExternalTicket, error) {
	if strings.TrimSpace(externalID) == "" {
		return nil, &TicketingError{Code: TicketingValidationError, Message: "externalTicketID is required"}
	}
	statusValue, err := k3gStatusWireValue(target)
	if err != nil {
		return nil, err
	}
	// mutating=true: same PRODUCT.6-K1 reasoning as CreateTicket — K3G
	// gives no way to tell whether a PUT that failed ambiguously (timeout,
	// 5xx, 429) actually committed.
	body, err := c.doOnce(ctx, http.MethodPut, "/api/support/tickets/"+externalID+"/status", map[string]int{"status": statusValue}, true)
	if err != nil {
		return nil, err
	}
	var resp k3gMutationResponse
	if err := json.Unmarshal(body, &resp); err != nil {
		return nil, &TicketingError{Code: TicketingWriteOutcomeUnknown, Message: "malformed status mutation response on a successful status", Err: err}
	}
	if !resp.OK {
		return nil, &TicketingError{Code: TicketingWriteOutcomeUnknown, Message: "provider returned ok=false for a 2xx status mutation response"}
	}
	ticket, err := resp.Ticket.toExternalTicket()
	if err != nil {
		// 2xx with no usable ticket.id: the provider claimed success but
		// gave no identity to record. Never silently substitute the
		// requested externalID — the caller's O2B3 reconciliation compares
		// the RETURNED id against durable local identity.
		return nil, &TicketingError{Code: TicketingWriteOutcomeUnknown, Message: "successful status mutation response is missing a usable ticket id", Err: err}
	}
	// Success integrity (PRODUCT.6-O2B2 section 9): a syntactically valid
	// 2xx whose returned status does not match the requested target is not
	// a trustworthy success — never pretend the mutation applied when the
	// provider's own echo disagrees.
	if ticket.ExternalStatus != target.Code {
		return nil, &TicketingError{Code: TicketingWriteOutcomeUnknown,
			Message: fmt.Sprintf("provider returned status %q for a mutation targeting %q", ticket.ExternalStatus, target.Code)}
	}
	return ticket, nil
}

// doOnce issues exactly one HTTP request and classifies the outcome into a
// *TicketingError on any non-2xx result or transport failure. "Once" is
// structural: there is no loop, no backoff, nothing here that could ever
// send a second request for a single doOnce call.
func (c *K3GTicketingConnector) doOnce(ctx context.Context, method, path string, jsonBody any, mutating bool) ([]byte, error) {
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
		// For a mutating call (PRODUCT.6-K1) the request may have reached
		// the provider before the transport failed, so the write outcome
		// is unknown, not "unavailable" (which implies safe-to-retry).
		if mutating {
			return nil, &TicketingError{Code: TicketingWriteOutcomeUnknown, Message: "transport error during a mutating request; write outcome unknown", Err: transportCause(err)}
		}
		return nil, &TicketingError{Code: TicketingProviderUnavailable, Message: "transport error", Err: transportCause(err)}
	}
	defer res.Body.Close()
	body, readErr := io.ReadAll(io.LimitReader(res.Body, 1<<20))
	if readErr != nil {
		if mutating {
			return nil, &TicketingError{Code: TicketingWriteOutcomeUnknown, Message: "failed to read response body of a mutating request; write outcome unknown", Err: readErr}
		}
		return nil, &TicketingError{Code: TicketingUnknownProviderError, Message: "failed to read response body", Err: readErr}
	}

	if res.StatusCode >= 200 && res.StatusCode < 300 {
		return body, nil
	}
	return nil, classifyK3GError(res.StatusCode, body, mutating)
}

// classifyK3GError maps an HTTP failure into the provider-neutral
// TicketingErrorCode model (PRODUCT.6-E), using K3G's stable "code" field
// (PRODUCT.6-C1 confirmed {"error":..., "code":"CRM_TICKET_NOT_MIGRATED"})
// in preference to matching on the human-readable Portuguese message.
// classifyK3GError maps an HTTP failure into the provider-neutral
// TicketingErrorCode model (PRODUCT.6-E), using K3G's stable "code" field
// (PRODUCT.6-C1 confirmed {"error":..., "code":"CRM_TICKET_NOT_MIGRATED"})
// in preference to matching on the human-readable Portuguese message.
//
// mutating (PRODUCT.6-K1) narrows 5xx and 429 to TicketingWriteOutcomeUnknown
// instead of TicketingProviderUnavailable: for CreateTicket, K3G gives no
// guarantee that a request answered with a server error or rate-limit was
// never processed, so these must be treated as a possible write, never as
// safe-to-retry. 400/401/403/404/422 stay definitive even when mutating —
// they are the provider explicitly rejecting the request before/without
// committing a ticket, not an ambiguous server-side failure.
func classifyK3GError(status int, body []byte, mutating bool) *TicketingError {
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
	case status == http.StatusUnprocessableEntity:
		// 422 "entidade compreendida mas semanticamente inválida" (K3G
		// OpenAPI, PRODUCT.6-O2A1) is a definitive semantic rejection, same
		// as 400 — the provider understood the request and refused it, no
		// ambiguity about whether a write occurred. No dedicated
		// TicketingErrorCode exists for this distinction (PRODUCT.6-O2B2:
		// inventing one without an observed real 422 response to justify a
		// different caller reaction would be speculative), so it shares
		// TicketingValidationError's category, never TicketingWriteOutcomeUnknown.
		return &TicketingError{Code: TicketingValidationError, Message: "request rejected as semantically invalid", Err: bodyErr(status, parsed.Error)}
	case status == http.StatusNotFound && parsed.Code == "CRM_TICKET_NOT_MIGRATED":
		return &TicketingError{Code: TicketingNotMigrated, Message: "ticket not yet available in CRM-native mode", Err: bodyErr(status, parsed.Error)}
	case status == http.StatusNotFound:
		return &TicketingError{Code: TicketingNotFound, Message: "ticket not found", Err: bodyErr(status, parsed.Error)}
	case status == http.StatusTooManyRequests:
		if mutating {
			return &TicketingError{Code: TicketingWriteOutcomeUnknown, Message: "rate limited on a mutating request; write outcome unknown", Err: bodyErr(status, parsed.Error)}
		}
		return &TicketingError{Code: TicketingProviderUnavailable, Message: "rate limited", Err: bodyErr(status, parsed.Error)}
	case status >= 500:
		if mutating {
			return &TicketingError{Code: TicketingWriteOutcomeUnknown, Message: "provider server error on a mutating request; write outcome unknown", Err: bodyErr(status, parsed.Error)}
		}
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
