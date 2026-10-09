package application

import (
	"context"
	"errors"
	"testing"

	"github.com/google/uuid"
	"github.com/omnira/omnira/internal/media/ports"
)

// ADR-0038 / Codex: while the company is suspended, nothing is fetched, scanned, sent to an AI provider or transcribed, and a
// result that arrives after the suspension is not stored. The repository's TenantGate is the only source of that answer.

type gatedRepo struct {
	*fakeRepo
	active bool
	err    error
	asked  int
}

func (g *gatedRepo) TenantActive(context.Context, uuid.UUID) (bool, error) {
	g.asked++
	return g.active, g.err
}

type gatedAnalysisRepo struct {
	*fakeAnalysisRepo
	// activeAfter[i] is the answer to the i-th question
	answers []bool
	asked   int
}

func (g *gatedAnalysisRepo) TenantActive(context.Context, uuid.UUID) (bool, error) {
	i := g.asked
	g.asked++
	if i >= len(g.answers) {
		return g.answers[len(g.answers)-1], nil
	}
	return g.answers[i], nil
}

func TestMediaIsNeitherFetchedNorScannedForASuspendedCompany(t *testing.T) {
	for name, gate := range map[string]*gatedRepo{
		"suspended":                 {active: false},
		"the question itself fails": {active: true, err: errors.New("db down")}, // fail closed
	} {
		w := newWork(ports.StatusPending)
		gate.fakeRepo = newRepo(w)
		f := &countingFetcher{data: pngData, mime: "image/png"}
		sc := &fakeScanner{}
		p, err := NewProcessor(gate, newStore(), f, sc, DefaultConfig(), nil)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := p.ProcessOnce(context.Background()); err != nil {
			t.Fatal(err)
		}
		if f.calls != 0 || sc.calls != 0 || gate.get(w.ID).work.Status != ports.StatusPending {
			t.Fatalf("%s: a file was fetched=%d scanned=%d status=%s", name, f.calls, sc.calls, gate.get(w.ID).work.Status)
		}
	}
	// the same file runs normally for an active company
	w := newWork(ports.StatusPending)
	gate := &gatedRepo{fakeRepo: newRepo(w), active: true}
	f := &countingFetcher{data: pngData, mime: "image/png"}
	p, _ := NewProcessor(gate, newStore(), f, &fakeScanner{}, DefaultConfig(), nil)
	if _, err := p.ProcessOnce(context.Background()); err != nil || f.calls != 1 || gate.get(w.ID).work.Status != ports.StatusClean {
		t.Fatalf("an active company's file must be processed: calls=%d err=%v", f.calls, err)
	}
}

type countingFetcher struct {
	data  []byte
	mime  string
	calls int
}

func (f *countingFetcher) Fetch(context.Context, string) ([]byte, string, error) {
	f.calls++
	return f.data, f.mime, nil
}

func TestVisionSendsNothingForASuspendedCompanyAndDoesNotStoreALateResult(t *testing.T) {
	// suspended from the start: no config lookup, no file read, no provider call
	r := newRig(t, optedIn(), imageWork())
	gate := &gatedAnalysisRepo{fakeAnalysisRepo: r.repo, answers: []bool{false}}
	p, _ := NewVisionProcessor(gate, r.vision, r.files, fakeTenants{cfg: optedIn()}, r.analyzer, r.ledger, nil)
	if _, err := p.ProcessOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	if r.analyzer.calls != 0 || r.files.read != 0 || len(r.ledger.rec) != 0 || r.repo.saved != nil {
		t.Fatalf("a suspended company's image went out: calls=%d reads=%d", r.analyzer.calls, r.files.read)
	}

	// suspended WHILE the provider was answering: the provider call happened (it cannot be undone) but the result is not stored
	r2 := newRig(t, optedIn(), imageWork())
	gate2 := &gatedAnalysisRepo{fakeAnalysisRepo: r2.repo, answers: []bool{true, false}}
	p2, _ := NewVisionProcessor(gate2, r2.vision, r2.files, fakeTenants{cfg: optedIn()}, r2.analyzer, r2.ledger, nil)
	if _, err := p2.ProcessOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	if r2.analyzer.calls != 1 || r2.repo.saved != nil {
		t.Fatalf("calls=%d saved=%v: the late result of a suspended company must not be stored", r2.analyzer.calls, r2.repo.saved)
	}
}

func TestTranscriptionDoesNothingForASuspendedCompanyAndDoesNotStoreALateResult(t *testing.T) {
	newT := func(answers []bool) (*gatedAnalysisRepo, *fakeEngine, *fakeFiles, *TranscriptionProcessor) {
		repo := &fakeAnalysisRepo{work: []ports.AnalysisWork{{ID: uuid.New(), TenantID: uuid.New(), MessageID: uuid.New(), MediaID: uuid.New(), Kind: "transcript", Attempts: 1, Mime: "audio/ogg"}}}
		gate := &gatedAnalysisRepo{fakeAnalysisRepo: repo, answers: answers}
		eng := &fakeEngine{a: ports.Analysis{Text: "olá, preciso de ajuda", Model: "whisper"}}
		files := &fakeFiles{data: []byte("OggS....")}
		p, err := NewTranscriptionProcessor(gate, files, eng, nil)
		if err != nil {
			t.Fatal(err)
		}
		return gate, eng, files, p
	}
	gate, eng, files, p := newT([]bool{false})
	if _, err := p.ProcessOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	if eng.calls != 0 || files.read != 0 || gate.saved != nil {
		t.Fatalf("a suspended company's audio was transcribed: calls=%d reads=%d", eng.calls, files.read)
	}
	gate, eng, _, p = newT([]bool{true, false})
	if _, err := p.ProcessOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	if eng.calls != 1 || gate.saved != nil {
		t.Fatalf("calls=%d saved=%v: a late transcript of a suspended company must not be stored", eng.calls, gate.saved)
	}
}
