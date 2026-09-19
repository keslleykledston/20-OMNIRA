package db_test

// TestRLSCompleteness — varredura de catálogo que prova que toda tabela
// tenant-owned tem RLS efetiva, em vez de confiar numa lista mantida à mão.
//
// Inspirado em tests/invariants/rls-completude-varredura.test.ts do
// DeskcommCRM (donor project, ver docs/research/deskcomm/REUSE-AUDIT.md) —
// a lição central de lá é que "relrowsecurity=true" sozinho não prova
// isolamento: uma tabela pode ter RLS "ligada" e mesmo assim ser
// completamente contornável (policy frouxa demais, ou FORCE ausente
// deixando o owner da conexão ignorá-la por completo — exatamente o bug
// real encontrado neste projeto antes desta suíte existir).
//
// O invariante verificado, para toda tabela em `public` com uma coluna
// `tenant_id`:
//  1. relrowsecurity = true (RLS habilitada);
//  2. relforcerowsecurity = true (RLS também vale para o dono da tabela —
//     sem isso, a role de aplicação, se algum dia se tornar owner ou rodar
//     como superuser por engano, contornaria tudo silenciosamente);
//  3. existe pelo menos uma policy registrada em pg_policies para a tabela.
//
// Novas tabelas tenant-owned devem ser adicionadas à migration de RLS
// (policies) e a FORCE ROW LEVEL SECURITY (migration 000005 ou seguinte)
// antes de merge — este teste falha o build enquanto isso não acontecer.
// Uma dívida conhecida e temporária pode ser registrada em
// knownRLSDebt abaixo, mas isso deve ser raro e sempre justificado em
// comentário (nome da issue/ticket).

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// knownRLSDebt — tabelas tenant-owned que ainda NÃO têm RLS completa, por
// motivo documentado. Vazio por padrão: qualquer entrada aqui é dívida
// visível, não uma aprovação silenciosa.
var knownRLSDebt = map[string]string{
	// "nome_da_tabela": "GAP(OMNIRA-123): motivo e prazo",
}

func TestRLSCompleteness(t *testing.T) {
	dbURL := os.Getenv("OMNIRA_DATABASE_URL")
	if dbURL == "" {
		t.Skip("OMNIRA_DATABASE_URL not set; skipping RLS completeness scan")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	pool, err := pgxpool.New(ctx, dbURL)
	if err != nil {
		t.Fatalf("failed to connect: %v", err)
	}
	defer pool.Close()

	const tenantTablesQuery = `
		SELECT c.relname, c.relrowsecurity, c.relforcerowsecurity
		FROM pg_class c
		JOIN pg_namespace n ON n.oid = c.relnamespace
		WHERE n.nspname = 'public'
		  AND c.relkind = 'r'
		  AND EXISTS (
		    SELECT 1 FROM information_schema.columns col
		    WHERE col.table_schema = 'public'
		      AND col.table_name = c.relname
		      AND col.column_name = 'tenant_id'
		  )
		ORDER BY c.relname
	`

	rows, err := pool.Query(ctx, tenantTablesQuery)
	if err != nil {
		t.Fatalf("failed to query pg_class: %v", err)
	}
	defer rows.Close()

	type tableRLS struct {
		name          string
		rowSecurity   bool
		forceSecurity bool
	}

	var tables []tableRLS
	for rows.Next() {
		var tr tableRLS
		if err := rows.Scan(&tr.name, &tr.rowSecurity, &tr.forceSecurity); err != nil {
			t.Fatalf("scan failed: %v", err)
		}
		tables = append(tables, tr)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("rows error: %v", err)
	}

	if len(tables) == 0 {
		t.Fatal("nenhuma tabela com coluna tenant_id encontrada — a query mudou ou o schema não foi migrado; isto não deveria acontecer num banco com migrations aplicadas")
	}

	for _, tr := range tables {
		if debt, known := knownRLSDebt[tr.name]; known {
			t.Logf("dívida conhecida registrada para %s: %s", tr.name, debt)
			continue
		}

		if !tr.rowSecurity {
			t.Errorf("tabela tenant-owned %q não tem ROW LEVEL SECURITY habilitada (relrowsecurity=false)", tr.name)
		}
		if !tr.forceSecurity {
			t.Errorf("tabela tenant-owned %q tem RLS habilitada mas NÃO tem FORCE (relforcerowsecurity=false) — inerte para o dono da tabela/conexões superuser", tr.name)
		}

		var policyCount int
		err := pool.QueryRow(ctx, `SELECT count(*) FROM pg_policies WHERE tablename = $1`, tr.name).Scan(&policyCount)
		if err != nil {
			t.Errorf("falha ao contar policies de %q: %v", tr.name, err)
			continue
		}
		if policyCount == 0 {
			t.Errorf("tabela tenant-owned %q tem RLS habilitada mas ZERO policies — nega tudo por padrão (fail-closed), o que é seguro mas quase certamente não é a intenção; crie ao menos uma policy explícita", tr.name)
		}
	}
}
