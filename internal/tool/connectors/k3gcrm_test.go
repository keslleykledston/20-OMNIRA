package connectors

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func crmServer(t *testing.T, handler http.HandlerFunc) *K3GCRMClient {
	t.Helper()
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	c, err := NewK3GCRMClient(K3GCRMConfig{BaseURL: srv.URL, Token: "tok"})
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func TestK3GCRMRequiresCompleteConfiguration(t *testing.T) {
	for _, cfg := range []K3GCRMConfig{
		{},
		{BaseURL: "https://api.example"},
		{Token: "t"},
		{BaseURL: "api.example", Token: "t"}, // sem esquema
	} {
		if _, err := NewK3GCRMClient(cfg); !errors.Is(err, ErrCRMNotConfigured) {
			t.Fatalf("configuração incompleta aceita: %+v", cfg)
		}
	}
}

func TestK3GCRMPingSendsBearerAndOnlyReads(t *testing.T) {
	var gotAuth, gotMethod, gotPath string
	c := crmServer(t, func(w http.ResponseWriter, r *http.Request) {
		gotAuth, gotMethod, gotPath = r.Header.Get("Authorization"), r.Method, r.URL.Path
		_ = json.NewEncoder(w).Encode(map[string]any{"users": []any{}, "total": 0})
	})
	if err := c.Ping(context.Background()); err != nil {
		t.Fatal(err)
	}
	if gotAuth != "Bearer tok" {
		t.Fatalf("token não foi no header Bearer: %q", gotAuth)
	}
	// "Testar" é um botão: a sonda não pode deixar rastro no CRM do cliente.
	if gotMethod != http.MethodGet {
		t.Fatalf("sonda escreveu no CRM: método %s em %s", gotMethod, gotPath)
	}
}

// 200 com corpo de outra coisa costuma ser portal de login ou proxy no
// caminho; aceitar esconderia uma integração quebrada.
func TestK3GCRMPingRejectsUnexpectedBody(t *testing.T) {
	c := crmServer(t, func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`<html><body>login</body></html>`))
	})
	if err := c.Ping(context.Background()); !errors.Is(err, ErrCRMRejected) {
		t.Fatalf("corpo inesperado aceito: %v", err)
	}
	c2 := crmServer(t, func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"algo": "diferente"})
	})
	if err := c2.Ping(context.Background()); !errors.Is(err, ErrCRMRejected) {
		t.Fatalf("JSON de outra rota aceito: %v", err)
	}
}

func TestK3GCRMMapsStatusToIntent(t *testing.T) {
	for _, tc := range []struct {
		status int
		want   error
	}{
		{http.StatusUnauthorized, ErrCRMUnauthorized},
		{http.StatusForbidden, ErrCRMUnauthorized},
		{http.StatusTooManyRequests, ErrCRMUnavailable},
		{http.StatusBadGateway, ErrCRMUnavailable},
		{http.StatusBadRequest, ErrCRMRejected},
	} {
		c := crmServer(t, func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(tc.status) })
		if err := c.Ping(context.Background()); !errors.Is(err, tc.want) {
			t.Fatalf("status %d → %v, esperado %v", tc.status, err, tc.want)
		}
	}
}

func TestK3GCRMListCompaniesNormalizesForTheOperator(t *testing.T) {
	c := crmServer(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			t.Errorf("listagem de empresas escreveu no CRM: %s", r.Method)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"companies": []map[string]any{
			{"id": "c1", "name": "Vision Telecom", "razaoSocial": "Vision LTDA", "cnpj": "111", "city": "Manaus", "state": "AM", "isActive": true},
			// Sem nome fantasia: o operador precisa enxergar a razão social.
			{"id": "c2", "name": "", "razaoSocial": "Somente Razao SA", "cnpj": "222", "isActive": false},
			// Sem id ou sem nenhum nome não é escolhível: viraria linha em branco.
			{"id": "", "name": "Sem id"},
			{"id": "c4", "name": "", "razaoSocial": ""},
		}})
	})
	companies, err := c.ListCompanies(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(companies) != 2 {
		t.Fatalf("esperava 2 empresas escolhíveis, veio %d: %+v", len(companies), companies)
	}
	if companies[0].Name != "Vision Telecom" {
		t.Fatalf("nome fantasia deveria ter precedência: %q", companies[0].Name)
	}
	if companies[1].Name != "Somente Razao SA" {
		t.Fatalf("sem nome fantasia, cai para razão social: %q", companies[1].Name)
	}
	// Inativa continua na lista, sinalizada: o operador decide, o código não
	// esconde cliente que pode estar em carência ou suspenso.
	if companies[1].IsActive {
		t.Fatal("isActive não foi preservado")
	}
}

func TestK3GCRMNeverLeaksTokenInErrors(t *testing.T) {
	c := crmServer(t, func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusInternalServerError) })
	err := c.Ping(context.Background())
	if err == nil {
		t.Fatal("esperava erro")
	}
	if strings.Contains(err.Error(), "tok") || strings.Contains(err.Error(), "Bearer") {
		t.Fatalf("credencial vazou no erro: %v", err)
	}
}
