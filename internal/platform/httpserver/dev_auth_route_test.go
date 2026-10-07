package httpserver

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

// Testar só a função de config não provaria nada sobre a superfície HTTP: o
// que importa é a rota não existir. Um handler que responde 403 ainda estaria
// publicado, e "404" é a única resposta que diz que não há nada ali.
func devLoginStatus(t *testing.T, devAuthEnabled bool) (int, string) {
	t.Helper()
	s := New("127.0.0.1:0")
	s.RegisterAuthHandlers(nil, devAuthEnabled, nil, 0)

	rec := httptest.NewRecorder()
	s.mux.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/v1/auth/dev/login", nil))

	modeRec := httptest.NewRecorder()
	s.mux.ServeHTTP(modeRec, httptest.NewRequest(http.MethodGet, "/api/v1/auth/mode", nil))
	return rec.Code, modeRec.Body.String()
}

func TestDevLoginRouteAbsentWhenDisabled(t *testing.T) {
	status, mode := devLoginStatus(t, false)
	if status != http.StatusNotFound {
		t.Fatalf("rota de dev login = %d, queria 404 (a rota não deve existir)", status)
	}

	var body struct {
		Mode    string `json:"mode"`
		DevAuth bool   `json:"dev_auth"`
	}
	if err := json.Unmarshal([]byte(mode), &body); err != nil {
		t.Fatalf("decode /auth/mode: %v", err)
	}
	if body.DevAuth {
		t.Error("/auth/mode anunciou dev_auth=true com o dev auth desligado")
	}
	if body.Mode == "dev" {
		t.Error("/auth/mode reportou modo dev com o dev auth desligado")
	}
}

func TestDevLoginRouteRegisteredWhenEnabled(t *testing.T) {
	status, mode := devLoginStatus(t, true)
	if status == http.StatusNotFound {
		t.Fatal("rota de dev login = 404 com o dev auth ligado")
	}

	var body struct {
		Mode    string `json:"mode"`
		DevAuth bool   `json:"dev_auth"`
	}
	if err := json.Unmarshal([]byte(mode), &body); err != nil {
		t.Fatalf("decode /auth/mode: %v", err)
	}
	if !body.DevAuth || body.Mode != "dev" {
		t.Errorf("/auth/mode = %+v, queria mode=dev dev_auth=true", body)
	}
}

// O path genérico saiu junto com o contrato falso de senha: manter os dois
// deixaria o acesso de desenvolvimento parecendo o login normal do produto.
func TestLegacyLoginRouteIsGone(t *testing.T) {
	for _, enabled := range []bool{false, true} {
		s := New("127.0.0.1:0")
		s.RegisterAuthHandlers(nil, enabled, nil, 0)
		rec := httptest.NewRecorder()
		s.mux.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/v1/auth/login", nil))
		if rec.Code != http.StatusNotFound {
			t.Errorf("devAuth=%v: POST /api/v1/auth/login = %d, queria 404", enabled, rec.Code)
		}
	}
}
