package meta

import (
	"context"

	"github.com/omnira/omnira/internal/channels/application"
)

// AccountProbe is the read-only account check behind "Testar conexão": the number's display phone, verified name and
// quality, and whether the WhatsApp Business Account has a webhook subscription. It writes nothing.
type AccountProbe struct{ client *Client }

var _ application.MetaAccountProbe = (*AccountProbe)(nil)

func NewAccountProbe(client *Client) *AccountProbe { return &AccountProbe{client: client} }

func (p *AccountProbe) Probe(ctx context.Context, token, phoneNumberID, wabaID string) (application.MetaAccountInfo, error) {
	info, err := p.client.PhoneInfo(ctx, token, phoneNumberID)
	if err != nil {
		return application.MetaAccountInfo{}, err
	}
	out := application.MetaAccountInfo{DisplayPhoneNumber: info.DisplayPhoneNumber, VerifiedName: info.VerifiedName, QualityRating: info.QualityRating, Status: info.Status, NameStatus: info.NameStatus}
	// The subscription is informational: a failure to read it never fails the credential check.
	if subscribed, err := p.client.WebhookSubscribed(ctx, token, wabaID); err == nil {
		out.WebhookSubscribed = &subscribed
	}
	return out, nil
}
