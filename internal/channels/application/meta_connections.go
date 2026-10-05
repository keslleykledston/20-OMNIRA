package application

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/omnira/omnira/internal/channels/domain"
	"github.com/omnira/omnira/internal/channels/ports"
	tenancydomain "github.com/omnira/omnira/internal/tenancy/domain"
)

// MetaAccountInfo is what the read-only probe learns about a WhatsApp Cloud number.
type MetaAccountInfo struct {
	DisplayPhoneNumber string
	VerifiedName       string
	QualityRating      string
	Status             string
	NameStatus         string
	// WebhookSubscribed is nil when it could not be read (never a reason to fail the credential check).
	WebhookSubscribed *bool
}

// MetaAccountProbe checks a token against the Graph API. It must be read-only: "Testar conexão" is a button.
type MetaAccountProbe interface {
	Probe(ctx context.Context, token, phoneNumberID, wabaID string) (MetaAccountInfo, error)
}

// ErrMetaNumberTaken: that phone number id is already connected (to this or another tenant: the answer is the same, so
// it is not an oracle).
var ErrMetaNumberTaken = errors.New("channel: that WhatsApp number is already connected")

var metaDigits = regexp.MustCompile(`^[0-9]{5,20}$`)

// MetaConnectionService manages official WhatsApp (Cloud API) numbers for the TenantContext tenant. Credentials live
// only encrypted; the verify token is generated here (the connection id is part of it so the unauthenticated webhook
// handshake can find the connection without a global secret). Every operation needs channel.manage.
type MetaConnectionService struct {
	descriptor  ports.ProviderDescriptor
	conns       ports.ChannelConnectionRepository
	credentials ports.CredentialStore
	perms       ports.PermissionChecker
	audit       ports.ChannelAudit
	probe       MetaAccountProbe
	callbackURL string
}

var _ TestableConnectionManager = (*MetaConnectionService)(nil)

func NewMetaConnectionService(descriptor ports.ProviderDescriptor, conns ports.ChannelConnectionRepository, credentials ports.CredentialStore,
	perms ports.PermissionChecker, audit ports.ChannelAudit, probe MetaAccountProbe, publicBaseURL string) *MetaConnectionService {
	return &MetaConnectionService{descriptor: descriptor, conns: conns, credentials: credentials, perms: perms, audit: audit, probe: probe,
		callbackURL: strings.TrimRight(publicBaseURL, "/") + "/webhooks/v1/whatsapp/meta"}
}

func (s *MetaConnectionService) authorize(ctx context.Context) (*tenancydomain.TenantContext, error) {
	tc, err := tenancydomain.FromContext(ctx)
	if err != nil || tc.TenantID == uuid.Nil || tc.ActorID == uuid.Nil || tc.Source != tenancydomain.AccessSourceDirect {
		return nil, ErrConnForbidden
	}
	ok, err := s.perms.HasPermission(ctx, tc.ActorID, PermissionChannelManage)
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, ErrConnForbidden
	}
	return tc, nil
}

// view adds what the operator must paste into the Meta app (callback URL and verify token) and what the last test
// learned. The access token and the app secret are never part of any view.
func (s *MetaConnectionService) view(ctx context.Context, c *domain.ChannelConnection) ConnectionView {
	v := view(c, "")
	v.Displays = map[string]string{"callback_url": s.callbackURL}
	if c.SecretRef != "" {
		if cred, err := s.credentials.Resolve(ctx, c.SecretRef); err == nil {
			for _, k := range []string{"verify_token", "display_phone_number", "verified_name", "quality_rating", "number_status", "webhook_subscribed"} {
				if cred.Fields[k] != "" {
					v.Displays[k] = cred.Fields[k]
				}
			}
		}
	}
	return v
}

func randomHex(n int) (string, error) {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}

func (s *MetaConnectionService) CreateConnection(ctx context.Context, req ConnectionCreateRequest) (ConnectionView, error) {
	if req.Provider != "" && req.Provider != s.descriptor.ID {
		return ConnectionView{}, ErrProviderNotFound
	}
	tc, err := s.authorize(ctx)
	if err != nil {
		return ConnectionView{}, err
	}
	phoneID, wabaID := strings.TrimSpace(req.Inputs["phone_number_id"]), strings.TrimSpace(req.Inputs["waba_id"])
	token, secret := strings.TrimSpace(req.Inputs["access_token"]), strings.TrimSpace(req.Inputs["app_secret"])
	switch {
	case !metaDigits.MatchString(phoneID):
		return ConnectionView{}, fmt.Errorf("%w: phone_number_id must be digits", ErrInvalidCredentials)
	case !metaDigits.MatchString(wabaID):
		return ConnectionView{}, fmt.Errorf("%w: waba_id must be digits", ErrInvalidCredentials)
	case len(token) < 20 || len(token) > 1000 || strings.ContainsAny(token, " \t\r\n"):
		return ConnectionView{}, fmt.Errorf("%w: access_token looks invalid", ErrInvalidCredentials)
	case len(secret) < 16 || len(secret) > 200 || strings.ContainsAny(secret, " \t\r\n"):
		return ConnectionView{}, fmt.Errorf("%w: app_secret looks invalid", ErrInvalidCredentials)
	}
	if existing, err := s.conns.FindByExternalNumberID(ctx, s.descriptor.ID, phoneID); err == nil && existing != nil {
		return ConnectionView{}, ErrMetaNumberTaken
	}
	id := uuid.New()
	rnd, err := randomHex(24)
	if err != nil {
		return ConnectionView{}, fmt.Errorf("channel: generate verify token: %w", err)
	}
	now := time.Now().UTC()
	conn := &domain.ChannelConnection{
		ID: id, TenantID: tc.TenantID, Channel: domain.ChannelWhatsApp, Provider: s.descriptor.ID, ProviderKind: s.descriptor.Kind,
		// phone_number_id is the trusted key the webhook resolves the tenant by; the WABA id is the account.
		ExternalNumberID: phoneID, ExternalAccountID: wabaID,
		Status:       domain.ConnectionStatusPending,
		Capabilities: append([]domain.Capability(nil), s.descriptor.Capabilities...),
		CreatedAt:    now, UpdatedAt: now,
	}
	if err := s.conns.Store(ctx, conn); err != nil {
		if errors.Is(err, ports.ErrDuplicateConnection) {
			return ConnectionView{}, ErrMetaNumberTaken
		}
		return ConnectionView{}, err
	}
	ref, err := s.credentials.Store(ctx, conn.ID, ports.Credential{Fields: map[string]string{
		"access_token": token, "app_secret": secret, "verify_token": "omn-" + id.String() + "-" + rnd, "phone_number_id": phoneID, "waba_id": wabaID,
	}})
	if err != nil {
		return ConnectionView{}, err
	}
	conn.SecretRef = ref
	if err := s.conns.Update(ctx, conn); err != nil {
		return ConnectionView{}, err
	}
	// provider and ids only: never a credential field
	if err := s.audit.Record(ctx, "channel.connection_created", "channel_connection", conn.ID, map[string]any{"provider": conn.Provider, "waba_id": wabaID}); err != nil {
		return ConnectionView{}, err
	}
	return s.view(ctx, conn), nil
}

func (s *MetaConnectionService) load(ctx context.Context, tc *tenancydomain.TenantContext, id uuid.UUID) (*domain.ChannelConnection, error) {
	conn, err := s.conns.FindByID(ctx, id)
	if err != nil {
		return nil, err
	}
	if conn == nil || conn.TenantID != tc.TenantID || conn.Provider != s.descriptor.ID {
		return nil, ErrConnNotFound
	}
	return conn, nil
}

func (s *MetaConnectionService) List(ctx context.Context) ([]ConnectionView, error) {
	tc, err := s.authorize(ctx)
	if err != nil {
		return nil, err
	}
	all, err := s.conns.FindByTenant(ctx, tc.TenantID)
	if err != nil {
		return nil, err
	}
	out := []ConnectionView{}
	for _, c := range all {
		if c.Provider == s.descriptor.ID {
			out = append(out, s.view(ctx, c))
		}
	}
	return out, nil
}

func (s *MetaConnectionService) Get(ctx context.Context, id uuid.UUID) (ConnectionView, error) {
	tc, err := s.authorize(ctx)
	if err != nil {
		return ConnectionView{}, err
	}
	conn, err := s.load(ctx, tc, id)
	if err != nil {
		return ConnectionView{}, err
	}
	return s.view(ctx, conn), nil
}

// TestConnection asks the Graph API (read-only) about the number behind the stored token and persists the REAL result:
// active only when Meta answered, failed when it refused. Without a probe the connection stays pending: "connected"
// is never claimed without having spoken to Meta.
func (s *MetaConnectionService) TestConnection(ctx context.Context, id uuid.UUID) (ConnectionView, error) {
	tc, err := s.authorize(ctx)
	if err != nil {
		return ConnectionView{}, err
	}
	conn, err := s.load(ctx, tc, id)
	if err != nil {
		return ConnectionView{}, err
	}
	if s.probe == nil {
		return ConnectionView{}, fmt.Errorf("%w: no probe for %s", ports.ErrNotConfigured, s.descriptor.ID)
	}
	cred, err := s.credentials.Resolve(ctx, conn.SecretRef)
	if err != nil {
		return ConnectionView{}, err
	}
	info, probeErr := s.probe.Probe(ctx, cred.Fields["access_token"], conn.ExternalNumberID, conn.ExternalAccountID)
	conn.Status = domain.ConnectionStatusActive
	outcome := "ok"
	if probeErr != nil {
		conn.Status, outcome = domain.ConnectionStatusFailed, "failed"
	}
	conn.UpdatedAt = time.Now().UTC()
	if err := s.conns.Update(ctx, conn); err != nil {
		return ConnectionView{}, err
	}
	if probeErr == nil {
		// what the number says about itself is shown to the operator (not secret); the secrets stay as they were
		cred.Fields["display_phone_number"], cred.Fields["verified_name"] = info.DisplayPhoneNumber, info.VerifiedName
		cred.Fields["quality_rating"], cred.Fields["number_status"] = info.QualityRating, info.Status
		delete(cred.Fields, "webhook_subscribed")
		if info.WebhookSubscribed != nil {
			cred.Fields["webhook_subscribed"] = fmt.Sprintf("%t", *info.WebhookSubscribed)
		}
		if err := s.credentials.Rotate(ctx, conn.SecretRef, cred); err != nil {
			return ConnectionView{}, err
		}
	}
	if err := s.audit.Record(ctx, "channel.connection_tested", "channel_connection", conn.ID, map[string]any{"provider": conn.Provider, "outcome": outcome}); err != nil {
		return ConnectionView{}, err
	}
	out := s.view(ctx, conn)
	out.CheckedAt = conn.UpdatedAt
	if probeErr != nil {
		if errors.Is(probeErr, ports.ErrAuthentication) {
			return out, fmt.Errorf("%w: %v", ErrCredentialRejected, probeErr)
		}
		return out, probeErr
	}
	return out, nil
}
