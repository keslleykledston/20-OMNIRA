package agent

// F9 (release/policies/signing.yaml "anti_replay.revocation.max_cache_age_seconds"): prova
// direta de Ledger.RevocationSyncFresh/MarkRevocationSynced/MarkStandalone — o relógio que
// isRevokedOrUnsafe (reconcile.go) consulta antes de confiar em "não revogada".
//
// Achado Codex [P1] (rodada 10, corrigindo a rodada 9): "nenhuma sincronização ainda" é
// FAIL-CLOSED por padrão, nunca "fresco por decreto" — a rodada 9 tratava isso como fresco
// para nunca bloquear uma instalação standalone, mas isso também deixava uma instalação
// GERENCIADA (API habilitada em algum lugar) cuja PRIMEIRA sincronização simplesmente ainda
// não aconteceu (ex. API fora do ar durante o bootstrap) "confiável" indefinidamente, sem
// NUNCA ter confirmado o estado real do Postgres. Só MarkStandalone — decisão EXPLÍCITA do
// operador (OMNIRA_AGENT_STANDALONE=true) — pode tornar essa ausência de sincronização
// fresca para sempre; sem essa marca, só MarkRevocationSynced (uma sincronização de verdade)
// destrava o relógio.

import (
	"context"
	"path/filepath"
	"testing"
	"time"
)

// backdateRevocationSync simula um relógio de sincronização antigo — o equivalente real seria
// o processo da API (updatemgr) ter ficado fora do ar, ou nunca ter conseguido entregar uma
// revogação pendente, por mais tempo que maxRevocationSyncAge, DEPOIS de já ter sincronizado
// ao menos uma vez antes. Upsert deliberado (nunca um UPDATE simples): funciona tanto para
// backdatar um relógio já existente quanto para criar um diretamente num estado "antigo", sem
// depender de uma inicialização automática que este pacote não faz mais (achado rodada 9).
func backdateRevocationSync(t *testing.T, ledger *Ledger, age time.Duration) {
	t.Helper()
	_, err := ledger.db.Exec(
		`INSERT INTO revocation_sync (id, synced_at) VALUES (1, ?) ON CONFLICT(id) DO UPDATE SET synced_at = excluded.synced_at`,
		time.Now().UTC().Add(-age).Format(time.RFC3339Nano))
	if err != nil {
		t.Fatalf("backdateRevocationSync: %v", err)
	}
}

func TestLedger_RevocationSyncFresh_NeverSynced_FailsClosedByDefault(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "ledger.db")
	ledger, err := OpenLedger(path)
	if err != nil {
		t.Fatalf("OpenLedger: %v", err)
	}
	defer ledger.Close()

	// Nenhuma sincronização aconteceu ainda, e nunca foi marcado standalone — o caso real de
	// uma instalação GERENCIADA cuja primeira sincronização simplesmente ainda não
	// aconteceu. Achado Codex [P1] (rodada 10): isso é fail-closed, nunca fresco por decreto.
	fresh, lastSync, err := ledger.RevocationSyncFresh(ctx, maxRevocationSyncAge)
	if err != nil {
		t.Fatalf("RevocationSyncFresh (nunca sincronizado, não-standalone): %v", err)
	}
	if fresh {
		t.Error("um ledger que NUNCA sincronizou e NUNCA foi marcado standalone deveria ser obsoleto (fail-closed), nunca fresco por decreto")
	}
	if !lastSync.IsZero() {
		t.Errorf("lastSync deveria ser zero quando nunca houve sincronização, veio %s", lastSync)
	}

	// MarkStandalone — decisão EXPLÍCITA do operador — torna essa mesma ausência de
	// sincronização fresca para sempre, a partir de agora.
	if err := ledger.MarkStandalone(ctx); err != nil {
		t.Fatalf("MarkStandalone: %v", err)
	}
	fresh, _, err = ledger.RevocationSyncFresh(ctx, maxRevocationSyncAge)
	if err != nil {
		t.Fatalf("RevocationSyncFresh (depois de MarkStandalone): %v", err)
	}
	if !fresh {
		t.Error("depois de MarkStandalone, o relógio deveria ser fresco mesmo sem nenhuma sincronização real")
	}

	// MarkStandalone continua fresco mesmo com o tempo passando — standalone nunca envelhece.
	backdateRevocationSync(t, ledger, maxRevocationSyncAge+time.Minute)
	if _, err := ledger.db.Exec(`UPDATE revocation_sync SET standalone = 1 WHERE id = 1`); err != nil {
		t.Fatalf("restaurar standalone=1 após backdate: %v", err)
	}
	fresh, _, err = ledger.RevocationSyncFresh(ctx, maxRevocationSyncAge)
	if err != nil {
		t.Fatalf("RevocationSyncFresh (standalone, relógio antigo): %v", err)
	}
	if !fresh {
		t.Error("uma instalação standalone nunca deveria ficar obsoleta, independente da idade do relógio")
	}
}

func TestLedger_RevocationSyncFresh_LifecycleAcrossMarkBackdateMark(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "ledger.db")
	ledger, err := OpenLedger(path)
	if err != nil {
		t.Fatalf("OpenLedger: %v", err)
	}
	defer ledger.Close()

	// Primeira sincronização de verdade.
	if err := ledger.MarkRevocationSynced(ctx); err != nil {
		t.Fatalf("MarkRevocationSynced: %v", err)
	}
	fresh, _, err := ledger.RevocationSyncFresh(ctx, maxRevocationSyncAge)
	if err != nil {
		t.Fatalf("RevocationSyncFresh (recém-sincronizado): %v", err)
	}
	if !fresh {
		t.Error("imediatamente depois de MarkRevocationSynced, o relógio deveria estar fresco")
	}

	backdateRevocationSync(t, ledger, maxRevocationSyncAge+time.Minute)
	fresh, lastSync, err := ledger.RevocationSyncFresh(ctx, maxRevocationSyncAge)
	if err != nil {
		t.Fatalf("RevocationSyncFresh (obsoleto): %v", err)
	}
	if fresh {
		t.Error("um relógio mais antigo que maxRevocationSyncAge, depois de JÁ ter sincronizado antes, nunca deveria ser reportado como fresco")
	}
	if time.Since(lastSync) < maxRevocationSyncAge {
		t.Errorf("lastSync deveria refletir o backdate aplicado, veio %s (idade real %s)", lastSync, time.Since(lastSync))
	}

	if err := ledger.MarkRevocationSynced(ctx); err != nil {
		t.Fatalf("MarkRevocationSynced (segunda vez): %v", err)
	}
	fresh, _, err = ledger.RevocationSyncFresh(ctx, maxRevocationSyncAge)
	if err != nil {
		t.Fatalf("RevocationSyncFresh (depois de ressincronizar): %v", err)
	}
	if !fresh {
		t.Error("depois de MarkRevocationSynced de novo, o relógio deveria estar fresco outra vez")
	}
}

func TestLedger_RevocationSyncFresh_ReopeningExistingFileNeverResetsAnAlreadySyncedClock(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "ledger.db")
	ledger, err := OpenLedger(path)
	if err != nil {
		t.Fatalf("OpenLedger (primeira abertura): %v", err)
	}
	// Precisa ter sincronizado ANTES de backdatar — achado rodada 9: este pacote não grava
	// mais nenhum baseline automático na criação do arquivo.
	if err := ledger.MarkRevocationSynced(ctx); err != nil {
		t.Fatalf("MarkRevocationSynced: %v", err)
	}
	backdateRevocationSync(t, ledger, maxRevocationSyncAge+time.Minute)
	ledger.Close()

	// Achado Codex [P1], F9 (rodada 7): OpenLedger é chamado com MUITA frequência em
	// produção (o processo da API abre/fecha o ledger por chamada) — se cada abertura
	// "resetasse" o relógio para agora, a checagem de frescor seria inútil (sempre pareceria
	// recém-sincronizado mesmo que o Postgres nunca tenha sido consultado de fato). Reabrir
	// o MESMO arquivo nunca deve apagar um relógio JÁ sincronizado antes, mesmo obsoleto.
	ledger2, err := OpenLedger(path)
	if err != nil {
		t.Fatalf("OpenLedger (reabertura): %v", err)
	}
	defer ledger2.Close()
	fresh, _, err := ledger2.RevocationSyncFresh(ctx, maxRevocationSyncAge)
	if err != nil {
		t.Fatalf("RevocationSyncFresh: %v", err)
	}
	if fresh {
		t.Error("reabrir um ledger com um relógio JÁ sincronizado (mesmo obsoleto) nunca deveria resetá-lo para fresco")
	}
}

// F9 (monitoramento "status do agente"): prova direta de Ledger.RecordAgentHeartbeat/
// AgentHeartbeat — o sinal de liveness do daemon, independente de agent_lock (que só existe
// ENQUANTO um run está ativo).
func TestLedger_AgentHeartbeat_LifecycleAcrossRecordAndRead(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "ledger.db")
	ledger, err := OpenLedger(path)
	if err != nil {
		t.Fatalf("OpenLedger: %v", err)
	}
	defer ledger.Close()

	// Nenhum heartbeat registrado ainda.
	_, found, err := ledger.AgentHeartbeat(ctx, "inst-1")
	if err != nil {
		t.Fatalf("AgentHeartbeat (antes de qualquer registro): %v", err)
	}
	if found {
		t.Error("nenhum heartbeat deveria ter sido encontrado antes de RecordAgentHeartbeat")
	}

	before := time.Now().UTC()
	if err := ledger.RecordAgentHeartbeat(ctx, "inst-1", "holder-a"); err != nil {
		t.Fatalf("RecordAgentHeartbeat: %v", err)
	}
	info, found, err := ledger.AgentHeartbeat(ctx, "inst-1")
	if err != nil {
		t.Fatalf("AgentHeartbeat: %v", err)
	}
	if !found {
		t.Fatal("deveria ter encontrado o heartbeat recém-registrado")
	}
	if info.Holder != "holder-a" {
		t.Errorf("holder = %q, esperava holder-a", info.Holder)
	}
	if info.LastPollAt.Before(before) {
		t.Errorf("lastPollAt (%s) deveria ser >= o instante antes do registro (%s)", info.LastPollAt, before)
	}
	if info.ObservedDigests != "{}" {
		t.Errorf("ObservedDigests deveria ser '{}' (nenhum snapshot gravado ainda), veio %q", info.ObservedDigests)
	}

	// Registrar de novo (ciclo seguinte do daemon) atualiza a MESMA linha, nunca cria outra.
	if err := ledger.RecordAgentHeartbeat(ctx, "inst-1", "holder-a"); err != nil {
		t.Fatalf("RecordAgentHeartbeat (segunda vez): %v", err)
	}
	var n int
	if err := ledger.db.QueryRow(`SELECT count(*) FROM agent_heartbeat WHERE installation_id = ?`, "inst-1").Scan(&n); err != nil {
		t.Fatalf("contar linhas de agent_heartbeat: %v", err)
	}
	if n != 1 {
		t.Errorf("esperava exatamente 1 linha de heartbeat para inst-1, achei %d", n)
	}

	// RecordObservedDigests (cadência independente) nunca sobrescreve last_poll_at.
	beforeObserve, _, _ := ledger.AgentHeartbeat(ctx, "inst-1")
	if err := ledger.RecordObservedDigests(ctx, "inst-1", "holder-a", `{"api":"ghcr.io/omnira/omnira-api@sha256:abc"}`); err != nil {
		t.Fatalf("RecordObservedDigests: %v", err)
	}
	afterObserve, _, err := ledger.AgentHeartbeat(ctx, "inst-1")
	if err != nil {
		t.Fatalf("AgentHeartbeat (depois de RecordObservedDigests): %v", err)
	}
	if !afterObserve.LastPollAt.Equal(beforeObserve.LastPollAt) {
		t.Errorf("RecordObservedDigests nunca deveria alterar last_poll_at: antes=%s depois=%s", beforeObserve.LastPollAt, afterObserve.LastPollAt)
	}
	if afterObserve.ObservedDigests != `{"api":"ghcr.io/omnira/omnira-api@sha256:abc"}` {
		t.Errorf("ObservedDigests = %q, não refletiu RecordObservedDigests", afterObserve.ObservedDigests)
	}
	if afterObserve.ObservedAt.IsZero() {
		t.Error("ObservedAt deveria ter sido gravado por RecordObservedDigests")
	}

	// Uma instalação DIFERENTE nunca é afetada.
	_, found, err = ledger.AgentHeartbeat(ctx, "inst-2")
	if err != nil {
		t.Fatalf("AgentHeartbeat (inst-2): %v", err)
	}
	if found {
		t.Error("inst-2 nunca deveria ter um heartbeat registrado")
	}
}
