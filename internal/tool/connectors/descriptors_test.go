package connectors

import (
	"strings"
	"testing"

	"github.com/omnira/omnira/internal/channels/ports"
)

// Um campo de credencial marcado como não-secreto volta pela API e aparece em
// tela e log. O teste existe para que essa marcação não se perca numa edição
// futura — é o tipo de regressão que passa despercebida em revisão.
func TestERPDescriptorsMarkCredentialFieldsAsSecret(t *testing.T) {
	sensitive := map[string]bool{"password": true, "token": true, "secret": true, "access_token": true, "app_secret": true}

	for _, d := range []ports.ProviderDescriptor{K3GCRMDescriptor(true, ""), IXCDescriptor(true, "")} {
		t.Run(d.ID, func(t *testing.T) {
			if d.ConnectMethod != ports.ConnectMethodCredentials {
				t.Fatalf("ERP deve usar formulário de credenciais, veio %q", d.ConnectMethod)
			}
			if len(d.Inputs) == 0 {
				t.Fatal("descritor sem campos: a tela não teria o que renderizar")
			}
			seen := map[string]bool{}
			for _, in := range d.Inputs {
				if in.Key == "" || in.Label == "" {
					t.Fatalf("campo sem key/label: %+v", in)
				}
				if seen[in.Key] {
					t.Fatalf("campo duplicado: %q", in.Key)
				}
				seen[in.Key] = true
				if sensitive[in.Key] && !in.Secret {
					t.Fatalf("campo %q carrega credencial e não está marcado como secret", in.Key)
				}
				if in.Secret && in.Example != "" {
					t.Fatalf("campo secreto %q traz exemplo, que vira placeholder em tela", in.Key)
				}
			}
			if !seen["base_url"] {
				t.Fatal("ERP precisa de base_url: o endereço não pode ser fixo no código")
			}
		})
	}
}

// Descritor desabilitado precisa dizer por quê, senão a opção some da tela sem
// explicação e o administrador não sabe o que fazer.
func TestDisabledERPDescriptorExplainsWhy(t *testing.T) {
	d := K3GCRMDescriptor(false, "CRM não configurado no servidor.")
	if d.Enabled {
		t.Fatal("deveria estar desabilitado")
	}
	if strings.TrimSpace(d.UnavailableReason) == "" {
		t.Fatal("desabilitado sem motivo visível ao administrador")
	}
}
