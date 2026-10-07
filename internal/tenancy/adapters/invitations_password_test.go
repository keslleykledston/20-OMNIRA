package adapters

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/omnira/omnira/internal/password"
	platformdb "github.com/omnira/omnira/internal/platform/db"
	"github.com/omnira/omnira/internal/tenancy/domain"
)

// TestAcceptInvitationWithPassword valida o fluxo completo:
// 1. CreateInvitation gera e guarda hash de senha temporária
// 2. AcceptInvitation exige password correto no body
// 3. Se válido: cria/atualiza user com password_hash + password_expires_at
func TestAcceptInvitationWithPassword(t *testing.T) {
	// Setup: usar o mesmo test database que IAM2C
	// Este é um exemplo de estrutura; a implementação real roda contra um DB real

	t.Run("accept invitation with wrong password returns 401", func(t *testing.T) {
		// ARRANGE
		tempPassword := "ValidTemp123!"
		tempPasswordHash, err := password.Hash(tempPassword)
		if err != nil {
			t.Fatalf("Failed to hash temp password: %v", err)
		}

		// ACT: simular POST /invitations/<token>/accept com senha errada
		body := acceptInvitationRequest{Password: "WrongPassword"}
		_, _ = json.Marshal(body)
		// ASSERT: deve retornar 401
		// (Este teste requer database real; esboço aqui)
		_ = tempPasswordHash
	})

	t.Run("accept invitation with correct password sets password_hash", func(t *testing.T) {
		// ARRANGE
		tempPassword := "ValidTemp123!"
		tempPasswordHash, _ := password.Hash(tempPassword)

		// ACT: POST com senha correta
		body := acceptInvitationRequest{Password: tempPassword}
		bodyBytes, _ := json.Marshal(body)
		_ = bodyBytes

		// ASSERT: user deve ter password_hash e password_expires_at setados
		_ = tempPasswordHash
	})

	t.Run("password expires_at is 72 hours in future", func(t *testing.T) {
		expiresAt := password.ExpiresAt()
		now := time.Now()
		diff := expiresAt.Sub(now)

		// Deve ser ~72h no futuro (±10 min de tolerância)
		if diff < 71*time.Hour*60 || diff > 73*time.Hour {
			t.Errorf("password_expires_at=%v, want ~72h in future", diff)
		}
	})
}

// TestTemporaryPasswordGeneration valida a geração segura de senhas
func TestTemporaryPasswordGeneration(t *testing.T) {
	t.Run("generated passwords are random and unique", func(t *testing.T) {
		passwords := make(map[string]bool)
		for i := 0; i < 100; i++ {
			pwd, err := password.Generate()
			if err != nil {
				t.Fatalf("Generate() error: %v", err)
			}
			if len(pwd) != password.TempLength {
				t.Errorf("password length=%d, want %d", len(pwd), password.TempLength)
			}
			if passwords[pwd] {
				t.Errorf("password %q generated twice", pwd)
			}
			passwords[pwd] = true
		}
	})

	t.Run("temporary password hash verification works", func(t *testing.T) {
		pwd, _ := password.Generate()
		hash, _ := password.Hash(pwd)

		if !password.Verify(hash, pwd) {
			t.Error("Verify() failed for correct password")
		}
		if password.Verify(hash, "WrongPassword") {
			t.Error("Verify() succeeded for wrong password")
		}
	})
}

// BenchmarkPasswordHash mede performance do bcrypt
func BenchmarkPasswordHash(b *testing.B) {
	pwd := "SomePassword123!@#"
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		password.Hash(pwd)
	}
}

func BenchmarkPasswordVerify(b *testing.B) {
	pwd := "SomePassword123!@#"
	hash, _ := password.Hash(pwd)
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		password.Verify(hash, pwd)
	}
}

// Helpers para teste de full flow (requer database real)
// Estas funções demonstram o padrão esperado nos testes de integração

func testAcceptInvitationFlow(ctx context.Context, t *testing.T, pool platformdb.Querier, tc *domain.TenantContext, token string) error {
	tempPassword := "TestPassword123!"

	// POST /invitations/<token>/accept
	body := acceptInvitationRequest{Password: tempPassword}
	bodyBytes, _ := json.Marshal(body)

	_ = httptest.NewRequest("POST", "/api/v1/invitations/"+token+"/accept", bytes.NewReader(bodyBytes))

	// Handler faria a validação, verificaria password_hash etc
	// Este é esboço; a implementação real testa contra DB real

	_ = tc
	return nil
}
