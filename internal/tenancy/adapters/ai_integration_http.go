package adapters

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/omnira/omnira/internal/aiusage"
	"io"
	"math"
	"net/http"
	"regexp"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	auditdomain "github.com/omnira/omnira/internal/audit/domain"
	auditports "github.com/omnira/omnira/internal/audit/ports"
	platformdb "github.com/omnira/omnira/internal/platform/db"
	tenancydomain "github.com/omnira/omnira/internal/tenancy/domain"
)

// SecretCipher is the slice of the credential cipher this handler needs (internal/channels/adapters/crypto.AESGCM
// satisfies it). The output of Encrypt carries its own random nonce.
type SecretCipher interface {
	Encrypt(plaintext []byte) ([]byte, error)
	Decrypt(ciphertext []byte) ([]byte, error)
}

// AIIntegrationHandler manages the per-tenant opt-in for the external AI provider (ADR-0016, decision 3).
//
// Rules, all enforced here and again by the table's CHECK / RLS:
//   - off by default; it can only be switched on by a tenant administrator who has supplied the key AND
//     explicitly accepted the current consent text (who and when are recorded);
//   - the API key is write-only: it is encrypted on arrival, never returned, never logged, never audited;
//   - removing the key switches the integration off.
type AIIntegrationHandler struct {
	pool   *pgxpool.Pool
	audit  auditports.AuditEventRepository
	cipher SecretCipher
	client *http.Client
	// geminiBase is the provider endpoint; a field so tests can point it at a local server.
	geminiBase string
	now        func() time.Time
	usage      UsageSummarizer
}

// UsageSummarizer reads the month's AI usage of a tenant (ADR-0017 Wave 10) inside the caller's session.
type UsageSummarizer interface {
	Summarize(ctx context.Context, tenantID uuid.UUID, month time.Time) (aiusage.Summary, error)
}

// WithUsage enables GET .../integrations/ai/usage.
func (h *AIIntegrationHandler) WithUsage(u UsageSummarizer) *AIIntegrationHandler {
	h.usage = u
	return h
}

type aiUsageLine struct {
	Provider     string  `json:"provider"`
	Model        string  `json:"model"`
	Task         string  `json:"task"`
	Calls        int     `json:"calls"`
	Failures     int     `json:"failures"`
	InputTokens  int64   `json:"input_tokens"`
	OutputTokens int64   `json:"output_tokens"`
	CostUSD      float64 `json:"estimated_cost_usd"`
}

// Usage: GET /tenants/{tenant_id}/integrations/ai/usage?month=YYYY-MM. Costs are ESTIMATES (the provider's invoice is the
// source of truth); tokens are what the providers reported.
func (h *AIIntegrationHandler) Usage(w http.ResponseWriter, r *http.Request) {
	tc, ok := h.authorize(w, r)
	if !ok {
		return
	}
	if h.usage == nil {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}
	month := h.now().UTC()
	if raw := r.URL.Query().Get("month"); raw != "" {
		m, err := time.Parse("2006-01", raw)
		if err != nil {
			http.Error(w, "invalid month (use YYYY-MM)", http.StatusBadRequest)
			return
		}
		month = m
	}
	sum, err := h.usage.Summarize(r.Context(), tc.TenantID, month)
	if err != nil {
		http.Error(w, "failed to read the usage", http.StatusInternalServerError)
		return
	}
	state, _, err := h.load(r.Context(), platformdb.QuerierFromContext(r.Context(), h.pool), tc.TenantID, false)
	if err != nil {
		http.Error(w, "failed to read the integration", http.StatusInternalServerError)
		return
	}
	lines := make([]aiUsageLine, 0, len(sum.Lines))
	var spent float64
	for _, l := range sum.Lines {
		lines = append(lines, aiUsageLine{Provider: l.Provider, Model: l.Model, Task: l.Task, Calls: l.Calls, Failures: l.Failures, InputTokens: l.InputTokens, OutputTokens: l.OutputTokens, CostUSD: l.CostUSD})
		if l.Provider == aiusage.BudgetProvider {
			spent += l.CostUSD
		}
	}
	remaining := state.BudgetUSD - spent
	if remaining < 0 {
		remaining = 0
	}
	writeAIJSON(w, http.StatusOK, map[string]any{
		"month": sum.From.Format("2006-01"), "from": sum.From.Format(time.RFC3339), "to": sum.To.Format(time.RFC3339),
		"budget_usd": state.BudgetUSD, "spent_usd": spent, "remaining_usd": remaining, "items": lines,
	})
}

const (
	aiProvider       = "gemini"
	defaultGeminiURL = "https://generativelanguage.googleapis.com"
	// AIConsentVersion identifies the exact consent text below. Changing the text requires a new version, which
	// makes every tenant accept again before the integration can be switched on.
	AIConsentVersion = "2026-10-gemini-v1"
	// AIConsentText is shown to the administrator and is the text they accept.
	AIConsentText = "Ao ativar, as imagens e os PDFs escaneados recebidos nas conversas desta organização serão enviados " +
		"à API do Google Gemini, usando a conta paga e a chave informada por você, para gerar a descrição e o texto " +
		"extraído. Segundo os termos do Gemini API para serviços pagos, o Google não usa esses dados para melhorar seus " +
		"produtos. Áudios continuam sendo transcritos no próprio servidor e não são enviados. A organização é " +
		"responsável por informar seus clientes sobre esse tratamento (LGPD)."
)

var (
	// "." is allowed: Google's newer Gemini keys carry a dot after a short prefix. The key only ever travels in a
	// header (never a URL), so the character is inert there.
	apiKeyPattern = regexp.MustCompile(`^[A-Za-z0-9_.\-]{20,200}$`)
	modelPattern  = regexp.MustCompile(`^[A-Za-z0-9._-]{1,100}$`)
)

func NewAIIntegrationHandler(pool *pgxpool.Pool, audit auditports.AuditEventRepository, cipher SecretCipher) *AIIntegrationHandler {
	return &AIIntegrationHandler{
		pool: pool, audit: audit, cipher: cipher,
		client: &http.Client{
			Timeout:       12 * time.Second,
			CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
		},
		geminiBase: defaultGeminiURL,
		now:        time.Now,
	}
}

// aiState is the full row minus the secret.
type aiState struct {
	Enabled         bool
	Model           string
	BudgetUSD       float64
	KeySet          bool
	KeySetAt        *time.Time
	KeySetByEmail   *string
	ConsentVersion  *string
	ConsentAt       *time.Time
	ConsentByEmail  *string
	LastTestAt      *time.Time
	LastTestOK      *bool
	LastTestMessage string
}

type aiResponse struct {
	Provider       string        `json:"provider"`
	Enabled        bool          `json:"enabled"`
	Model          string        `json:"model"`
	BudgetUSD      float64       `json:"monthly_budget_usd"`
	KeyConfigured  bool          `json:"key_configured"`
	KeySetAt       *string       `json:"key_set_at,omitempty"`
	KeySetBy       *string       `json:"key_set_by,omitempty"`
	ConsentText    string        `json:"consent_text"`
	ConsentVersion string        `json:"consent_version"`
	ConsentCurrent bool          `json:"consent_current"`
	ConsentAt      *string       `json:"consent_at,omitempty"`
	ConsentBy      *string       `json:"consent_by,omitempty"`
	LastTest       *aiTestResult `json:"last_test,omitempty"`
}

type aiTestResult struct {
	At             string `json:"at"`
	OK             bool   `json:"ok"`
	Message        string `json:"message"`
	ModelAvailable *bool  `json:"model_available,omitempty"`
}

func (h *AIIntegrationHandler) authorize(w http.ResponseWriter, r *http.Request) (*tenancydomain.TenantContext, bool) {
	tc, err := (&TeamHandler{pool: h.pool}).authorize(r, "tenant.manage")
	if err != nil {
		respondAuthzError(w, err)
		return nil, false
	}
	return tc, true
}

func rfc(t *time.Time) *string {
	if t == nil {
		return nil
	}
	s := t.UTC().Format(time.RFC3339)
	return &s
}

func (h *AIIntegrationHandler) toResponse(s aiState) aiResponse {
	out := aiResponse{
		Provider: aiProvider, Enabled: s.Enabled, Model: s.Model, BudgetUSD: s.BudgetUSD,
		KeyConfigured: s.KeySet, KeySetAt: rfc(s.KeySetAt), KeySetBy: s.KeySetByEmail,
		ConsentText: AIConsentText, ConsentVersion: AIConsentVersion,
		ConsentCurrent: s.ConsentVersion != nil && *s.ConsentVersion == AIConsentVersion && s.ConsentAt != nil,
		ConsentAt:      rfc(s.ConsentAt), ConsentBy: s.ConsentByEmail,
	}
	if s.LastTestAt != nil {
		out.LastTest = &aiTestResult{At: *rfc(s.LastTestAt), OK: s.LastTestOK != nil && *s.LastTestOK, Message: s.LastTestMessage}
	}
	return out
}

func defaultAIState() aiState { return aiState{Model: "gemini-2.5-flash", BudgetUSD: 10} }

// load reads the row (without the ciphertext) or returns the defaults when the tenant never configured it.
func (h *AIIntegrationHandler) load(ctx context.Context, q platformdb.Querier, tenant uuid.UUID, lock bool) (aiState, bool, error) {
	suffix := ""
	if lock {
		suffix = " FOR UPDATE OF t"
	}
	var s aiState
	err := q.QueryRow(ctx, `
		SELECT t.enabled, t.model, t.monthly_budget_usd::float8, t.secret_ciphertext IS NOT NULL, t.secret_set_at, ku.email,
		       t.consent_version, t.consent_at, cu.email, t.last_test_at, t.last_test_ok, t.last_test_message
		FROM tenant_ai_integrations t
		LEFT JOIN users ku ON ku.id = t.secret_set_by
		LEFT JOIN users cu ON cu.id = t.consent_by
		WHERE t.tenant_id = $1 AND t.provider = $2`+suffix, tenant, aiProvider).
		Scan(&s.Enabled, &s.Model, &s.BudgetUSD, &s.KeySet, &s.KeySetAt, &s.KeySetByEmail,
			&s.ConsentVersion, &s.ConsentAt, &s.ConsentByEmail, &s.LastTestAt, &s.LastTestOK, &s.LastTestMessage)
	if errors.Is(err, pgx.ErrNoRows) {
		return defaultAIState(), false, nil
	}
	return s, err == nil, err
}

func (h *AIIntegrationHandler) Get(w http.ResponseWriter, r *http.Request) {
	tc, ok := h.authorize(w, r)
	if !ok {
		return
	}
	s, _, err := h.load(r.Context(), platformdb.QuerierFromContext(r.Context(), h.pool), tc.TenantID, false)
	if err != nil {
		http.Error(w, "failed to read the integration", http.StatusInternalServerError)
		return
	}
	writeAIJSON(w, http.StatusOK, h.toResponse(s))
}

func writeAIJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

type aiPutRequest struct {
	Enabled          *bool    `json:"enabled"`
	Model            *string  `json:"model"`
	BudgetUSD        *float64 `json:"monthly_budget_usd"`
	APIKey           *string  `json:"api_key"`
	AcceptExternalAI *bool    `json:"accept_external_ai"`
}

// Put applies a partial update. Every invariant is checked before anything is written, in one transaction.
func (h *AIIntegrationHandler) Put(w http.ResponseWriter, r *http.Request) {
	tc, ok := h.authorize(w, r)
	if !ok {
		return
	}
	var req aiPutRequest
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&req); err != nil {
		http.Error(w, "invalid request body", http.StatusBadRequest)
		return
	}
	if msg := validateAIPut(req); msg != "" {
		http.Error(w, msg, http.StatusUnprocessableEntity)
		return
	}

	ctx := r.Context()
	q := platformdb.QuerierFromContext(ctx, h.pool)
	prev, exists, err := h.load(ctx, q, tc.TenantID, true)
	if err != nil {
		http.Error(w, "failed to read the integration", http.StatusInternalServerError)
		return
	}
	next := prev
	keyChanged := false
	var newCipher []byte
	if req.APIKey != nil {
		blob, err := h.cipher.Encrypt([]byte(strings.TrimSpace(*req.APIKey)))
		if err != nil {
			http.Error(w, "failed to protect the key", http.StatusInternalServerError)
			return
		}
		newCipher, keyChanged = blob, true
		next.KeySet = true
	}
	if req.Model != nil {
		next.Model = *req.Model
	}
	if req.BudgetUSD != nil {
		next.BudgetUSD = math.Round(*req.BudgetUSD*100) / 100
	}
	consentRecorded := false
	if req.Enabled != nil {
		next.Enabled = *req.Enabled
	}
	if next.Enabled {
		if !next.KeySet {
			http.Error(w, "informe a chave da API antes de ativar", http.StatusUnprocessableEntity)
			return
		}
		current := prev.ConsentVersion != nil && *prev.ConsentVersion == AIConsentVersion && prev.ConsentAt != nil
		if !current {
			if req.AcceptExternalAI == nil || !*req.AcceptExternalAI {
				http.Error(w, "é preciso aceitar o termo de envio de dados ao provedor externo para ativar", http.StatusUnprocessableEntity)
				return
			}
			consentRecorded = true
		}
	}

	now := h.now().UTC()
	uid := tc.ActorID
	if exists {
		_, err = q.Exec(ctx, `
			UPDATE tenant_ai_integrations SET
			  enabled = $3, model = $4, monthly_budget_usd = $5,
			  secret_ciphertext = CASE WHEN $6 THEN $7::bytea ELSE secret_ciphertext END,
			  secret_set_at     = CASE WHEN $6 THEN $8::timestamptz ELSE secret_set_at END,
			  secret_set_by     = CASE WHEN $6 THEN $9::uuid ELSE secret_set_by END,
			  consent_version   = CASE WHEN $10 THEN $11::text ELSE consent_version END,
			  consent_at        = CASE WHEN $10 THEN $8::timestamptz ELSE consent_at END,
			  consent_by        = CASE WHEN $10 THEN $9::uuid ELSE consent_by END,
			  updated_at = $8::timestamptz
			WHERE tenant_id = $1 AND provider = $2`,
			tc.TenantID, aiProvider, next.Enabled, next.Model, next.BudgetUSD,
			keyChanged, newCipher, now, uid, consentRecorded, AIConsentVersion)
	} else {
		// A brand-new row: values are written as they are (the CHECK sees the final row).
		var secretAt *time.Time
		var secretBy, consentBy *uuid.UUID
		var consentVersion *string
		var consentAt *time.Time
		if keyChanged {
			secretAt, secretBy = &now, &uid
		}
		if consentRecorded {
			v := AIConsentVersion
			consentVersion, consentAt, consentBy = &v, &now, &uid
		}
		_, err = q.Exec(ctx, `
			INSERT INTO tenant_ai_integrations (tenant_id, provider, enabled, model, monthly_budget_usd,
			       secret_ciphertext, secret_set_at, secret_set_by, consent_version, consent_at, consent_by, updated_at)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12)`,
			tc.TenantID, aiProvider, next.Enabled, next.Model, next.BudgetUSD,
			newCipher, secretAt, secretBy, consentVersion, consentAt, consentBy, now)
	}
	if err != nil {
		http.Error(w, "failed to save the integration", http.StatusInternalServerError)
		return
	}
	h.record(r, tc, prev, next, keyChanged, consentRecorded, exists)
	saved, _, err := h.load(ctx, q, tc.TenantID, false)
	if err != nil {
		http.Error(w, "failed to read the integration", http.StatusInternalServerError)
		return
	}
	writeAIJSON(w, http.StatusOK, h.toResponse(saved))
}

func validateAIPut(req aiPutRequest) string {
	if req.APIKey != nil && !apiKeyPattern.MatchString(strings.TrimSpace(*req.APIKey)) {
		return "a chave da API tem formato inválido"
	}
	if req.Model != nil && !modelPattern.MatchString(*req.Model) {
		return "nome de modelo inválido"
	}
	if req.BudgetUSD != nil && (math.IsNaN(*req.BudgetUSD) || *req.BudgetUSD < 0 || *req.BudgetUSD > 10000) {
		return "o orçamento mensal deve estar entre 0 e 10000"
	}
	return ""
}

// DeleteKey removes the key and, with it, the ability to run: the integration is switched off.
func (h *AIIntegrationHandler) DeleteKey(w http.ResponseWriter, r *http.Request) {
	tc, ok := h.authorize(w, r)
	if !ok {
		return
	}
	ctx := r.Context()
	q := platformdb.QuerierFromContext(ctx, h.pool)
	prev, _, err := h.load(ctx, q, tc.TenantID, true)
	if err != nil {
		http.Error(w, "failed to read the integration", http.StatusInternalServerError)
		return
	}
	if _, err := q.Exec(ctx, `
		UPDATE tenant_ai_integrations
		SET secret_ciphertext = NULL, secret_set_at = NULL, secret_set_by = NULL, enabled = false,
		    last_test_at = NULL, last_test_ok = NULL, last_test_message = '', updated_at = now()
		WHERE tenant_id = $1 AND provider = $2`, tc.TenantID, aiProvider); err != nil {
		http.Error(w, "failed to remove the key", http.StatusInternalServerError)
		return
	}
	next := prev
	next.KeySet, next.Enabled = false, false
	h.record(r, tc, prev, next, true, false, true)
	saved, _, _ := h.load(ctx, q, tc.TenantID, false)
	writeAIJSON(w, http.StatusOK, h.toResponse(saved))
}

// Test checks the stored key against the provider's model list. The key travels in a header (never a URL),
// the target is a fixed host, redirects are refused, and the provider's body never reaches the client.
func (h *AIIntegrationHandler) Test(w http.ResponseWriter, r *http.Request) {
	tc, ok := h.authorize(w, r)
	if !ok {
		return
	}
	ctx := r.Context()
	q := platformdb.QuerierFromContext(ctx, h.pool)
	var blob []byte
	var model string
	var lastTest *time.Time
	err := q.QueryRow(ctx, `SELECT secret_ciphertext, model, last_test_at FROM tenant_ai_integrations WHERE tenant_id=$1 AND provider=$2 FOR UPDATE`,
		tc.TenantID, aiProvider).Scan(&blob, &model, &lastTest)
	if errors.Is(err, pgx.ErrNoRows) || (err == nil && len(blob) == 0) {
		http.Error(w, "nenhuma chave configurada", http.StatusConflict)
		return
	}
	if err != nil {
		http.Error(w, "failed to read the integration", http.StatusInternalServerError)
		return
	}
	if lastTest != nil && h.now().Sub(*lastTest) < 3*time.Second {
		http.Error(w, "aguarde alguns segundos entre testes", http.StatusTooManyRequests)
		return
	}
	key, err := h.cipher.Decrypt(blob)
	if err != nil {
		http.Error(w, "failed to read the key", http.StatusInternalServerError)
		return
	}
	ok2, available, msg := h.probe(ctx, string(key), model)
	now := h.now().UTC()
	_, _ = q.Exec(ctx, `UPDATE tenant_ai_integrations SET last_test_at=$3, last_test_ok=$4, last_test_message=$5, updated_at=now() WHERE tenant_id=$1 AND provider=$2`,
		tc.TenantID, aiProvider, now, ok2, msg)
	res := aiTestResult{At: now.Format(time.RFC3339), OK: ok2, Message: msg}
	if ok2 {
		res.ModelAvailable = &available
	}
	writeAIJSON(w, http.StatusOK, res)
}

// probe never returns provider text: only fixed, safe messages.
func (h *AIIntegrationHandler) probe(ctx context.Context, key, model string) (ok, modelAvailable bool, message string) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, h.geminiBase+"/v1beta/models?pageSize=1000", nil)
	if err != nil {
		return false, false, "não foi possível preparar o teste"
	}
	req.Header.Set("x-goog-api-key", key)
	req.Header.Set("Accept", "application/json")
	res, err := h.client.Do(req)
	if err != nil {
		return false, false, "não foi possível falar com o Google agora"
	}
	defer res.Body.Close()
	switch {
	case res.StatusCode == http.StatusOK:
	case res.StatusCode == http.StatusBadRequest, res.StatusCode == http.StatusUnauthorized, res.StatusCode == http.StatusForbidden:
		return false, false, "o Google recusou a chave (inválida, revogada ou sem permissão)"
	case res.StatusCode == http.StatusTooManyRequests:
		return false, false, "limite de uso do Google atingido; tente mais tarde"
	default:
		return false, false, fmt.Sprintf("o Google respondeu com erro %d", res.StatusCode)
	}
	var body struct {
		Models []struct {
			Name string `json:"name"`
		} `json:"models"`
	}
	if err := json.NewDecoder(io.LimitReader(res.Body, 4<<20)).Decode(&body); err != nil {
		return true, false, "chave aceita, mas a lista de modelos não pôde ser lida"
	}
	for _, m := range body.Models {
		if m.Name == "models/"+model {
			return true, true, "chave válida e modelo disponível"
		}
	}
	return true, false, "chave válida, mas o modelo informado não está disponível para ela"
}

func (h *AIIntegrationHandler) record(r *http.Request, tc *tenancydomain.TenantContext, prev, next aiState, keyChanged, consent, existed bool) {
	if h.audit == nil {
		return
	}
	ev, err := auditdomain.NewAuditEvent(tc.TenantID, tc.ActorID, auditdomain.ActionTenantAIIntegration, auditdomain.ResourceTenant, tc.TenantID, auditdomain.OutcomeSuccess, uuid.Nil)
	if err != nil {
		return
	}
	// Flags and values only; the key itself is never an argument here.
	ev.SetMetadata("provider", aiProvider)
	ev.SetMetadata("enabled_from", prev.Enabled)
	ev.SetMetadata("enabled_to", next.Enabled)
	ev.SetMetadata("model_from", prev.Model)
	ev.SetMetadata("model_to", next.Model)
	ev.SetMetadata("budget_from", prev.BudgetUSD)
	ev.SetMetadata("budget_to", next.BudgetUSD)
	ev.SetMetadata("key_changed", keyChanged)
	ev.SetMetadata("key_configured", next.KeySet)
	ev.SetMetadata("consent_recorded", consent)
	if consent {
		ev.SetMetadata("consent_version", AIConsentVersion)
	}
	_ = h.audit.Store(r.Context(), ev)
}
