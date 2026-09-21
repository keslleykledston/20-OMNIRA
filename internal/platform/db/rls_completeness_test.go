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

// expectedPolicyCoverage — para cada tabela crítica, os comandos que a
// aplicação realmente executa e que portanto precisam de policy.
//
// A varredura acima prova que existe *alguma* policy; não prova que existe
// policy para a operação que o código usa. Foi por aí que dois defeitos
// passaram: users tinha SELECT e UPDATE mas não INSERT, e o JIT provisioning
// do primeiro login falhava sob FORCE RLS; conversation_participants entrou
// sem policy nenhuma. Ambos só apareceram em teste contra Postgres real.
//
// Listar a operação aqui é declarar "o runtime faz isto" — e o inverso também
// vale: o que não está listado não deve ganhar policy sem motivo.
var expectedPolicyCoverage = map[string][]string{
	// users não tem tenant_id, então a varredura por tenant_id não a alcança.
	"users":                     {"SELECT", "UPDATE", "INSERT"},
	"user_identities":           {"SELECT", "INSERT", "UPDATE"},
	"conversation_participants": {"SELECT", "INSERT", "UPDATE"},
	"contacts":                  {"SELECT", "INSERT", "UPDATE", "DELETE"},
	"conversations":             {"SELECT", "INSERT", "UPDATE", "DELETE"},
	"messages":                  {"SELECT", "INSERT", "UPDATE", "DELETE"},
}

func TestRLSPolicyCoverage(t *testing.T) {
	dbURL := os.Getenv("OMNIRA_DATABASE_URL")
	if dbURL == "" {
		t.Skip("OMNIRA_DATABASE_URL not set; skipping RLS policy coverage scan")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	pool, err := pgxpool.New(ctx, dbURL)
	if err != nil {
		t.Fatalf("failed to connect: %v", err)
	}
	defer pool.Close()

	// polcmd: r=SELECT, a=INSERT, w=UPDATE, d=DELETE, *=ALL
	commandOf := map[string]string{"r": "SELECT", "a": "INSERT", "w": "UPDATE", "d": "DELETE"}

	for table, expected := range expectedPolicyCoverage {
		rows, err := pool.Query(ctx, `
			SELECT p.polcmd::text
			FROM pg_policy p
			JOIN pg_class c ON c.oid = p.polrelid
			JOIN pg_namespace n ON n.oid = c.relnamespace
			WHERE n.nspname = 'public' AND c.relname = $1`, table)
		if err != nil {
			t.Fatalf("query policies for %s: %v", table, err)
		}
		covered := map[string]bool{}
		for rows.Next() {
			var cmd string
			if err := rows.Scan(&cmd); err != nil {
				rows.Close()
				t.Fatalf("scan policy for %s: %v", table, err)
			}
			if cmd == "*" {
				for _, c := range commandOf {
					covered[c] = true
				}
				continue
			}
			if c, ok := commandOf[cmd]; ok {
				covered[c] = true
			}
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			t.Fatalf("rows error for %s: %v", table, err)
		}

		if len(covered) == 0 {
			t.Errorf("tabela %q não tem policy alguma, mas o runtime executa %v", table, expected)
			continue
		}
		for _, cmd := range expected {
			if !covered[cmd] {
				t.Errorf("tabela %q não tem policy de %s, mas o runtime executa essa operação — sob FORCE RLS ela falha em produção", table, cmd)
			}
		}
	}
}
