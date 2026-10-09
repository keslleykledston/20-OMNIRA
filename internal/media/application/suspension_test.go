package application

import (
	"context"
	"errors"
	"testing"

	"github.com/google/uuid"
	"github.com/omnira/omnira/internal/media/ports"
)

// ADR-0038: while the company is suspended nothing is fetched, scanned, sent to an AI provider or transcribed; while it is active,
// EVERY external call and EVERY write of the operation (all result branches) happens INSIDE the gate's WhileActive, i.e. under the
// lock a suspension has to wait for. The repository's TenantGate is the only source of both facts.

type gate struct {
	active bool
	err    error
	inside bool
	ran    int
}

func (g *gate) WhileActive(ctx context.Context, _ uuid.UUID, fn func(context.Context) error) (bool, error) {
	if g.err != nil || !g.active {
		return false, g.err
	}
	g.ran++
	g.inside = true
	defer func() { g.inside = false }()
	return true, fn(ctx)
}

type gatedRepo struct {
	*fakeRepo
	gate
}

type gatedAnalysisRepo struct {
	*fakeAnalysisRepo
	gate
	t        *testing.T
	outside  int // writes made outside WhileActive
	callsOut int
}

func (g *gatedAnalysisRepo) check() {
	if !g.inside {
		g.outside++
	}
}
func (g *gatedAnalysisRepo) SaveAnalysis(ctx context.Context, w ports.AnalysisWork, a ports.Analysis) error {
	g.check()
	return g.fakeAnalysisRepo.SaveAnalysis(ctx, w, a)
}
func (g *gatedAnalysisRepo) SaveAnalysisEmpty(ctx context.Context, w ports.AnalysisWork, reason string) error {
	g.check()
	return g.fakeAnalysisRepo.SaveAnalysisEmpty(ctx, w, reason)
}
func (g *gatedAnalysisRepo) FailAnalysis(ctx context.Context, w ports.AnalysisWork, reason string) error {
	g.check()
	return g.fakeAnalysisRepo.FailAnalysis(ctx, w, reason)
}

// insideAnalyzer / insideEngine record a call made outside the gate.
type insideAnalyzer struct {
	inner *fakeAnalyzer
	g     *gatedAnalysisRepo
}

func (a insideAnalyzer) Analyze(ctx context.Context, key, model string, in ports.VisionInput) (ports.Analysis, ports.Usage, error) {
	if !a.g.inside {
		a.g.callsOut++
	}
	return a.inner.Analyze(ctx, key, model, in)
}

type insideEngine struct {
	inner *fakeEngine
	g     *gatedAnalysisRepo
}

func (e insideEngine) Transcribe(ctx context.Context, data []byte, mime string) (ports.Analysis, error) {
	if !e.g.inside {
		e.g.callsOut++
	}
	return e.inner.Transcribe(ctx, data, mime)
}

type countingFetcher struct {
	data  []byte
	mime  string
	calls int
	g     *gate
	out   int
}

func (f *countingFetcher) Fetch(context.Context, string) ([]byte, string, error) {
	f.calls++
	if f.g != nil && !f.g.inside {
		f.out++
	}
	return f.data, f.mime, nil
}

func TestMediaIsNeitherFetchedNorScannedForASuspendedCompany(t *testing.T) {
	for name, g := range map[string]gate{
		"suspended":                 {active: false},
		"the question itself fails": {active: true, err: errors.New("db down")}, // fail closed
	} {
		w := newWork(ports.StatusPending)
		repo := &gatedRepo{fakeRepo: newRepo(w), gate: g}
		f := &countingFetcher{data: pngData, mime: "image/png"}
		sc := &fakeScanner{}
		p, err := NewProcessor(repo, newStore(), f, sc, DefaultConfig(), nil)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := p.ProcessOnce(context.Background()); err != nil {
			t.Fatal(err)
		}
		if f.calls != 0 || sc.calls != 0 || repo.get(w.ID).work.Status != ports.StatusPending {
			t.Fatalf("%s: a file was fetched=%d scanned=%d status=%s", name, f.calls, sc.calls, repo.get(w.ID).work.Status)
		}
	}
	// the same file runs normally for an active company, and the fetch AND the antivirus run inside the gate
	w := newWork(ports.StatusPending)
	repo := &gatedRepo{fakeRepo: newRepo(w), gate: gate{active: true}}
	f := &countingFetcher{data: pngData, mime: "image/png", g: &repo.gate}
	sc := &fakeScanner{}
	p, _ := NewProcessor(repo, newStore(), f, sc, DefaultConfig(), nil)
	if _, err := p.ProcessOnce(context.Background()); err != nil || f.calls != 1 || repo.get(w.ID).work.Status != ports.StatusClean {
		t.Fatalf("an active company's file must be processed: calls=%d err=%v", f.calls, err)
	}
	if f.out != 0 || repo.ran == 0 {
		t.Fatalf("the fetch ran outside the gate (out=%d, gate runs=%d)", f.out, repo.ran)
	}
}

func TestVisionRunsEveryProviderCallAndEveryWriteInsideTheGateAndNothingWhenSuspended(t *testing.T) {
	cases := map[string]func(*fakeAnalyzer){
		"text":       func(a *fakeAnalyzer) {},
		"unreadable": func(a *fakeAnalyzer) { a.err = ports.ErrNothingReadable },
		"rejected":   func(a *fakeAnalyzer) { a.err = ports.ErrProviderRejected },
		"transient":  func(a *fakeAnalyzer) { a.err = errors.New("provider down") },
	}
	for name, tweak := range cases {
		r := newRig(t, optedIn(), imageWork())
		g := &gatedAnalysisRepo{fakeAnalysisRepo: r.repo, gate: gate{active: true}, t: t}
		tweak(r.analyzer)
		p, _ := NewVisionProcessor(g, r.vision, r.files, fakeTenants{cfg: optedIn()}, insideAnalyzer{inner: r.analyzer, g: g}, r.ledger, nil)
		if _, err := p.ProcessOnce(context.Background()); err != nil {
			t.Fatal(err)
		}
		if r.analyzer.calls != 1 || g.ran == 0 || g.callsOut != 0 || g.outside != 0 {
			t.Fatalf("%s: calls=%d gate runs=%d provider calls outside=%d writes outside=%d", name, r.analyzer.calls, g.ran, g.callsOut, g.outside)
		}
	}
	// suspended: no config lookup, no file read, no provider call, nothing stored
	r := newRig(t, optedIn(), imageWork())
	g := &gatedAnalysisRepo{fakeAnalysisRepo: r.repo, gate: gate{active: false}, t: t}
	p, _ := NewVisionProcessor(g, r.vision, r.files, fakeTenants{cfg: optedIn()}, insideAnalyzer{inner: r.analyzer, g: g}, r.ledger, nil)
	if _, err := p.ProcessOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	if r.analyzer.calls != 0 || r.files.read != 0 || len(r.ledger.rec) != 0 || r.repo.saved != nil || r.repo.empty != "" || r.repo.failed != "" {
		t.Fatalf("a suspended company's image went out or was recorded: calls=%d reads=%d", r.analyzer.calls, r.files.read)
	}
}

func TestTranscriptionRunsEveryEngineCallAndEveryWriteInsideTheGateAndNothingWhenSuspended(t *testing.T) {
	newT := func(active bool, tweak func(*fakeEngine)) (*gatedAnalysisRepo, *fakeEngine, *fakeFiles, *TranscriptionProcessor) {
		repo := &fakeAnalysisRepo{work: []ports.AnalysisWork{{ID: uuid.New(), TenantID: uuid.New(), MessageID: uuid.New(), MediaID: uuid.New(), Kind: "transcript", Attempts: 1, Mime: "audio/ogg"}}}
		g := &gatedAnalysisRepo{fakeAnalysisRepo: repo, gate: gate{active: active}, t: t}
		eng := &fakeEngine{a: ports.Analysis{Text: "olá, preciso de ajuda", Model: "whisper"}}
		tweak(eng)
		files := &fakeFiles{data: []byte("OggS....")}
		p, err := NewTranscriptionProcessor(g, files, insideEngine{inner: eng, g: g}, nil)
		if err != nil {
			t.Fatal(err)
		}
		return g, eng, files, p
	}
	for name, tweak := range map[string]func(*fakeEngine){
		"text":      func(e *fakeEngine) {},
		"no speech": func(e *fakeEngine) { e.err = ports.ErrNoSpeech },
		"error":     func(e *fakeEngine) { e.err = errors.New("engine down") },
	} {
		g, eng, _, p := newT(true, tweak)
		if _, err := p.ProcessOnce(context.Background()); err != nil {
			t.Fatal(err)
		}
		if eng.calls != 1 || g.ran == 0 || g.callsOut != 0 || g.outside != 0 {
			t.Fatalf("%s: calls=%d gate runs=%d engine calls outside=%d writes outside=%d", name, eng.calls, g.ran, g.callsOut, g.outside)
		}
	}
	g, eng, files, p := newT(false, func(*fakeEngine) {})
	if _, err := p.ProcessOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	if eng.calls != 0 || files.read != 0 || g.saved != nil || g.empty != "" || g.failed != "" || g.retried != "" {
		t.Fatalf("a suspended company's audio was transcribed or recorded: calls=%d reads=%d", eng.calls, files.read)
	}
}
