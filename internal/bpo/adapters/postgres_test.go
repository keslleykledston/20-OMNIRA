package adapters

import (
	"testing"
)

func TestPostgresAdaptersCompile(t *testing.T) {
	// Verifica que adaptadores compilam
	// Testes reais requerem banco de dados
	_ = NewPostgresAccountRepository(nil)
	_ = NewPostgresTicketRepository(nil)
}
