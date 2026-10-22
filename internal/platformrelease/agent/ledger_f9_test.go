package agent

// F9 (release/policies/signing.yaml "anti_replay.revocation.max_cache_age_seconds"): prova
// direta de Ledger.RevocationSyncFresh/MarkRevocationSynced — o relógio que isRevokedOrUnsafe
// (reconcile.go) consulta antes de confiar em "não revogada". Um ledger recém-criado começa
// fresco (nada a perder ainda); backdatar o relógio manualmente (simulando a API inteira fora
// do ar por mais tempo do que qualquer retry individual cobre) o torna obsoleto; marcar
// synced de novo o torna fresco outra vez.

import (
	"context"
	"path/filepath"
	"testing"
	"time"
)

// backdateRevocationSync simula um relógio de sincronização antigo — o equivalente real
// seria o processo da API (updatemgr) ter ficado fora do ar, ou nunca ter conseguido entregar
// uma revogação pendente, por mais tempo que maxRevocationSyncAge.
func backdateRevocationSync(t *testing.T, ledger *Ledger, age time.Duration) {
	t.Helper()
	_, err := ledger.db.Exec(`UPDATE revocation_sync SET synced_at = ? WHERE id = 1`,
		time.Now().UTC().Add(-age).Format(time.RFC3339Nano))
	if err != nil {
		t.Fatalf("backdateRevocationSync: %v", err)
	}
}

func TestLedger_RevocationSyncFresh_LifecycleAcrossCreateBackdateMark(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "ledger.db")
	ledger, err := OpenLedger(path)
	if err != nil {
		t.Fatalf("OpenLedger: %v", err)
	}
	defer ledger.Close()

	// Um ledger recém-criado começa fresco — não há nenhuma revogação para ter perdido
	// ainda, bloquear a própria primeira execução seria desproporcional.
	fresh, _, err := ledger.RevocationSyncFresh(ctx, maxRevocationSyncAge)
	if err != nil {
		t.Fatalf("RevocationSyncFresh (recém-criado): %v", err)
	}
	if !fresh {
		t.Error("um ledger recém-criado deveria começar com a sincronização de revogação fresca")
	}

	backdateRevocationSync(t, ledger, maxRevocationSyncAge+time.Minute)
	fresh, lastSync, err := ledger.RevocationSyncFresh(ctx, maxRevocationSyncAge)
	if err != nil {
		t.Fatalf("RevocationSyncFresh (obsoleto): %v", err)
	}
	if fresh {
		t.Error("um relógio mais antigo que maxRevocationSyncAge nunca deveria ser reportado como fresco")
	}
	if time.Since(lastSync) < maxRevocationSyncAge {
		t.Errorf("lastSync deveria refletir o backdate aplicado, veio %s (idade real %s)", lastSync, time.Since(lastSync))
	}

	if err := ledger.MarkRevocationSynced(ctx); err != nil {
		t.Fatalf("MarkRevocationSynced: %v", err)
	}
	fresh, _, err = ledger.RevocationSyncFresh(ctx, maxRevocationSyncAge)
	if err != nil {
		t.Fatalf("RevocationSyncFresh (depois de MarkRevocationSynced): %v", err)
	}
	if !fresh {
		t.Error("depois de MarkRevocationSynced, o relógio deveria estar fresco de novo")
	}
}

func TestLedger_RevocationSyncFresh_ReopeningExistingFileNeverResetsTheClock(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "ledger.db")
	ledger, err := OpenLedger(path)
	if err != nil {
		t.Fatalf("OpenLedger (primeira abertura): %v", err)
	}
	backdateRevocationSync(t, ledger, maxRevocationSyncAge+time.Minute)
	ledger.Close()

	// Achado Codex [P1], F9 (rodada 7): OpenLedger é chamado com MUITA frequência em
	// produção (o processo da API abre/fecha o ledger por chamada) — se cada abertura
	// "resetasse" o relógio para agora, a checagem de frescor seria inútil (sempre pareceria
	// recém-sincronizado mesmo que o Postgres nunca tenha sido consultado de fato). Reabrir
	// o MESMO arquivo nunca deve apagar um backdate já gravado.
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
		t.Error("reabrir um ledger EXISTENTE nunca deveria resetar o relógio de sincronização de revogação")
	}
}
