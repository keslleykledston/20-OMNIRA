package waha

import (
	"context"
	"errors"
	"strings"

	"github.com/omnira/omnira/internal/channels/domain"
	"github.com/omnira/omnira/internal/channels/ports"
)

// SessionController adapts WahaProvider to ports.SessionController.
type SessionController struct{ provider *WahaProvider }

var _ ports.SessionController = (*SessionController)(nil)

func NewSessionController(provider *WahaProvider) *SessionController {
	return &SessionController{provider: provider}
}

func (c *SessionController) Status(ctx context.Context, conn domain.ChannelConnection) (ports.SessionStatus, error) {
	session, err := c.provider.GetSession(ctx, conn)
	if err != nil {
		// WAHA answers 404 for an unknown session; the client reports every 4xx
		// (except auth/rate-limit/conflict) as ErrPermanent. Auth, rate limit and
		// availability errors keep their own classification and propagate.
		if errors.Is(err, ErrPermanent) && !errors.Is(err, ErrAuthentication) && !errors.Is(err, ErrSessionDisconnected) {
			return ports.SessionMissing, nil
		}
		return "", err
	}
	switch strings.ToUpper(session.Status) {
	case "WORKING":
		return ports.SessionWorking, nil
	case "SCAN_QR_CODE":
		return ports.SessionNeedsQR, nil
	case "STARTING":
		return ports.SessionStarting, nil
	case "STOPPED":
		return ports.SessionStopped, nil
	default:
		return ports.SessionFailed, nil
	}
}

func (c *SessionController) Create(ctx context.Context, conn domain.ChannelConnection, webhookBaseURL string) error {
	_, err := c.provider.CreateSessionWithWebhook(ctx, conn, webhookBaseURL)
	return err
}

func (c *SessionController) Start(ctx context.Context, conn domain.ChannelConnection) error {
	_, err := c.provider.StartSession(ctx, conn)
	return err
}

func (c *SessionController) Stop(ctx context.Context, conn domain.ChannelConnection) error {
	_, err := c.provider.StopSession(ctx, conn)
	return err
}

func (c *SessionController) QR(ctx context.Context, conn domain.ChannelConnection) (ports.QRImage, error) {
	qr, err := c.provider.GetQRCode(ctx, conn)
	if err != nil {
		return ports.QRImage{}, err
	}
	return ports.QRImage{MIMEType: qr.MIMEType, Data: qr.Data}, nil
}

func (c *SessionController) Account(ctx context.Context, conn domain.ChannelConnection) (string, error) {
	me, err := c.provider.GetMe(ctx, conn)
	if err != nil || me == nil {
		return "", err
	}
	id := me.ID
	if i := strings.IndexByte(id, '@'); i >= 0 {
		id = id[:i]
	}
	return id, nil
}
