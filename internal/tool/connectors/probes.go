package connectors

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/omnira/omnira/internal/channels/ports"
)

// asPortError traduz o erro do connector para o vocabulário da porta, que é o
// que as camadas de cima sabem interpretar. Sem isso, credencial recusada
// chegaria ao operador como "erro interno do servidor".
func asPortError(err error) error {
	switch {
	case err == nil:
		return nil
	case errors.Is(err, ErrCRMUnauthorized):
		return fmt.Errorf("%w: %v", ports.ErrAuthentication, err)
	case errors.Is(err, ErrCRMNotConfigured), errors.Is(err, ErrIXCNotConfigured):
		return fmt.Errorf("%w: %v", ports.ErrNotConfigured, err)
	case errors.Is(err, ErrCRMUnavailable), errors.Is(err, ErrIXCUnavailable):
		return fmt.Errorf("%w: %v", ports.ErrProviderUnavailable, err)
	case errors.Is(err, ErrIXCNotFound), errors.Is(err, ErrCRMRejected), errors.Is(err, ErrIXCRejected):
		return fmt.Errorf("%w: %v", ports.ErrPermanent, err)
	default:
		return err
	}
}

// Sondas usadas pela aba de Integrações para verificar credencial de ERP.
// Cada uma só lê: "Testar" é um botão, e o operador clica mais de uma vez.

// K3GCRMProbe verifica a credencial do CRM próprio da K3G.
type K3GCRMProbe struct{ Timeout time.Duration }

func (p K3GCRMProbe) Probe(ctx context.Context, fields map[string]string) error {
	client, err := NewK3GCRMClient(K3GCRMConfig{
		BaseURL: fields["base_url"], Token: fields["token"], Timeout: p.Timeout,
	})
	if err != nil {
		return asPortError(err)
	}
	return asPortError(client.Ping(ctx))
}

// IXCProbe verifica a credencial do ERP IXC.
type IXCProbe struct{ Timeout time.Duration }

func (p IXCProbe) Probe(ctx context.Context, fields map[string]string) error {
	connector, err := NewIXCConnector(IXCConfig{
		BaseURL: fields["base_url"], User: fields["user"], Token: fields["token"], Timeout: p.Timeout,
	})
	if err != nil {
		return asPortError(err)
	}
	return asPortError(connector.Authenticate(ctx, nil))
}
