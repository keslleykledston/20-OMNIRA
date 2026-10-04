package application

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/omnira/omnira/internal/media/ports"
)

type row struct {
	work     ports.Work
	reason   string
	retryIn  time.Duration
	q        ports.Quarantined
	purged   bool
	terminal bool
}

type fakeRepo struct {
	mu   sync.Mutex
	rows map[uuid.UUID]*row
}

func newRepo(ws ...ports.Work) *fakeRepo {
	r := &fakeRepo{rows: map[uuid.UUID]*row{}}
	for _, w := range ws {
		w := w
		r.rows[w.ID] = &row{work: w}
	}
	return r
}

func (r *fakeRepo) Claim(_ context.Context, limit int, _ time.Duration) ([]ports.Work, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	var out []ports.Work
	for _, x := range r.rows {
		if !x.terminal && x.retryIn == 0 && (x.work.Status == ports.StatusPending || x.work.Status == ports.StatusQuarantined) && len(out) < limit {
			x.work.Attempts++
			out = append(out, x.work)
		}
	}
	return out, nil
}
func (r *fakeRepo) MarkQuarantined(_ context.Context, w ports.Work, q ports.Quarantined) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	x := r.rows[w.ID]
	x.work.Status, x.q = ports.StatusQuarantined, q
	return nil
}
func (r *fakeRepo) MarkClean(_ context.Context, w ports.Work) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.rows[w.ID].work.Status, r.rows[w.ID].terminal = ports.StatusClean, true
	return nil
}
func (r *fakeRepo) MarkTerminal(_ context.Context, w ports.Work, s ports.Status, reason string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	x := r.rows[w.ID]
	x.work.Status, x.reason, x.terminal = s, reason, true
	return nil
}
func (r *fakeRepo) Retry(_ context.Context, w ports.Work, d time.Duration, reason string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.rows[w.ID].retryIn, r.rows[w.ID].reason = d, reason
	return nil
}
func (r *fakeRepo) ExpiredFiles(_ context.Context, before time.Time, _ int) ([]ports.Work, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	var out []ports.Work
	for _, x := range r.rows {
		if !x.purged && x.work.CreatedAt.Before(before) {
			out = append(out, x.work)
		}
	}
	return out, nil
}
func (r *fakeRepo) MarkPurged(_ context.Context, w ports.Work) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.rows[w.ID].purged = true
	return nil
}
func (r *fakeRepo) get(id uuid.UUID) row { r.mu.Lock(); defer r.mu.Unlock(); return *r.rows[id] }

type fakeStore struct {
	mu         sync.Mutex
	quarantine map[uuid.UUID][]byte
	clean      map[uuid.UUID][]byte
}

func newStore() *fakeStore {
	return &fakeStore{quarantine: map[uuid.UUID][]byte{}, clean: map[uuid.UUID][]byte{}}
}
func (s *fakeStore) PutQuarantine(w ports.Work, d []byte) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.quarantine[w.ID] = d
	return nil
}
func (s *fakeStore) GetQuarantine(w ports.Work) ([]byte, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	d, ok := s.quarantine[w.ID]
	if !ok {
		return nil, errors.New("missing")
	}
	return d, nil
}
func (s *fakeStore) Promote(w ports.Work) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.clean[w.ID] = s.quarantine[w.ID]
	delete(s.quarantine, w.ID)
	return nil
}
func (s *fakeStore) Remove(w ports.Work) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.quarantine, w.ID)
	delete(s.clean, w.ID)
	return nil
}

type fakeFetcher struct {
	data []byte
	mime string
	err  error
}

func (f fakeFetcher) Fetch(context.Context, string) ([]byte, string, error) {
	return f.data, f.mime, f.err
}

type fakeScanner struct {
	verdict ports.Verdict
	err     error
	panics  bool
	calls   int
}

func (s *fakeScanner) Scan(context.Context, []byte) (ports.Verdict, error) {
	s.calls++
	if s.panics {
		panic("scanner exploded")
	}
	return s.verdict, s.err
}

var pngData = []byte("\x89PNG\r\n\x1a\n\x00\x00\x00\rIHDR\x00\x00\x00\x01\x00\x00\x00\x01\x08\x00\x00\x00\x00:~\x9bU\x00\x00\x00\nIDATx\x9cc`\x00\x00\x00\x02\x00\x01H\xaf\xa4q\x00\x00\x00\x00IEND\xaeB`\x82")

func newWork(status ports.Status) ports.Work {
	return ports.Work{ID: uuid.New(), TenantID: uuid.New(), MessageID: uuid.New(), Status: status, MediaRef: "http://localhost:3000/api/files/s/x", CreatedAt: time.Now().Add(-time.Hour)}
}

func run(t *testing.T, w ports.Work, f fakeFetcher, sc *fakeScanner, cfg Config) (*Processor, *fakeRepo, *fakeStore) {
	t.Helper()
	repo, store := newRepo(w), newStore()
	p, err := NewProcessor(repo, store, f, sc, cfg, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := p.ProcessOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	return p, repo, store
}

func TestCleanFileIsPublishedOnlyAfterTheAntivirusClearsIt(t *testing.T) {
	w := newWork(ports.StatusPending)
	sc := &fakeScanner{}
	_, repo, store := run(t, w, fakeFetcher{data: pngData, mime: "image/png"}, sc, DefaultConfig())
	r := repo.get(w.ID)
	if r.work.Status != ports.StatusClean || sc.calls != 1 {
		t.Fatalf("status=%s scans=%d", r.work.Status, sc.calls)
	}
	if r.q.Mime != "image/png" || r.q.Kind != "image" || r.q.SHA256 == "" || r.q.SizeBytes != int64(len(pngData)) {
		t.Fatalf("quarantine metadata = %+v", r.q)
	}
	if _, ok := store.clean[w.ID]; !ok || len(store.quarantine) != 0 {
		t.Fatal("a clean file must move from quarantine to the served area")
	}
}

func TestInfectedFileIsNeverPublishedAndItsBytesAreDeleted(t *testing.T) {
	w := newWork(ports.StatusPending)
	sc := &fakeScanner{verdict: ports.Verdict{Infected: true, Signature: "Win.Test.EICAR_HDB-1"}}
	_, repo, store := run(t, w, fakeFetcher{data: pngData, mime: "image/png"}, sc, DefaultConfig())
	r := repo.get(w.ID)
	if r.work.Status != ports.StatusInfected || r.reason != "Win.Test.EICAR_HDB-1" {
		t.Fatalf("status=%s reason=%q", r.work.Status, r.reason)
	}
	if len(store.clean) != 0 || len(store.quarantine) != 0 {
		t.Fatal("an infected file must not be kept anywhere")
	}
}

func TestAntivirusDownFailsClosedAndKeepsTheFileQuarantined(t *testing.T) {
	w := newWork(ports.StatusPending)
	sc := &fakeScanner{err: errors.New("connection refused")}
	_, repo, store := run(t, w, fakeFetcher{data: pngData, mime: "image/png"}, sc, DefaultConfig())
	r := repo.get(w.ID)
	if r.work.Status != ports.StatusQuarantined || r.terminal || r.retryIn == 0 {
		t.Fatalf("must stay quarantined and be rescheduled: %+v", r)
	}
	if len(store.clean) != 0 {
		t.Fatal("an unscanned file must never reach the served area")
	}
	if _, ok := store.quarantine[w.ID]; !ok {
		t.Fatal("the bytes must be kept in quarantine for the retry (WAHA deletes its copy in minutes)")
	}
}

func TestAntivirusStillDownAfterAllAttemptsEndsFailedNotClean(t *testing.T) {
	w := newWork(ports.StatusQuarantined)
	w.Attempts = 59
	repo, store := newRepo(w), newStore()
	store.quarantine[w.ID] = pngData
	p, _ := NewProcessor(repo, store, fakeFetcher{}, &fakeScanner{err: errors.New("down")}, DefaultConfig(), nil)
	if _, err := p.ProcessOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	r := repo.get(w.ID)
	if r.work.Status != ports.StatusFailed || r.reason != "antivirus_unavailable" {
		t.Fatalf("status=%s reason=%q", r.work.Status, r.reason)
	}
	if len(store.clean) != 0 {
		t.Fatal("never clean without a verdict")
	}
}

func TestDisallowedTypeIsRejectedWithoutReachingTheAntivirusOrDisk(t *testing.T) {
	cases := map[string][]byte{
		"zip":   append([]byte("PK\x03\x04"), make([]byte, 30)...),
		"exe":   append([]byte("MZ\x90\x00"), make([]byte, 30)...),
		"html":  []byte("<html><script>alert(1)</script>"),
		"svg":   []byte(`<svg xmlns="http://www.w3.org/2000/svg"/>`),
		"bytes": {1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12},
	}
	for name, data := range cases {
		t.Run(name, func(t *testing.T) {
			w := newWork(ports.StatusPending)
			sc := &fakeScanner{}
			_, repo, store := run(t, w, fakeFetcher{data: data, mime: "image/jpeg"}, sc, DefaultConfig())
			if r := repo.get(w.ID); r.work.Status != ports.StatusRejected {
				t.Fatalf("status = %s", r.work.Status)
			}
			if sc.calls != 0 || len(store.quarantine) != 0 || len(store.clean) != 0 {
				t.Fatal("a rejected file must not be stored or scanned")
			}
		})
	}
}

func TestProviderNoLongerHavingTheFileIsFinalOnlyAfterTheGraceWindow(t *testing.T) {
	young := newWork(ports.StatusPending)
	young.CreatedAt = time.Now().Add(-5 * time.Second)
	_, repo, _ := run(t, young, fakeFetcher{err: ports.ErrSourceGone}, &fakeScanner{}, DefaultConfig())
	if r := repo.get(young.ID); r.terminal || r.retryIn == 0 {
		t.Fatalf("a 5s-old message must be retried, got %+v", r)
	}
	old := newWork(ports.StatusPending)
	_, repo, _ = run(t, old, fakeFetcher{err: ports.ErrSourceGone}, &fakeScanner{}, DefaultConfig())
	if r := repo.get(old.ID); r.work.Status != ports.StatusSourceGone {
		t.Fatalf("an old message must end source_gone, got %s", r.work.Status)
	}
}

func TestTransientFetchErrorsRetryThenFail(t *testing.T) {
	w := newWork(ports.StatusPending)
	_, repo, _ := run(t, w, fakeFetcher{err: errors.New("timeout")}, &fakeScanner{}, DefaultConfig())
	if r := repo.get(w.ID); r.terminal || r.retryIn == 0 {
		t.Fatalf("first failure must be retried: %+v", r)
	}
	last := newWork(ports.StatusPending)
	last.Attempts = 7 // Claim makes it the 8th, the last allowed
	_, repo, _ = run(t, last, fakeFetcher{err: errors.New("timeout")}, &fakeScanner{}, DefaultConfig())
	if r := repo.get(last.ID); r.work.Status != ports.StatusFailed {
		t.Fatalf("exhausted retries must end failed, got %s", r.work.Status)
	}
}

func TestAPanicOnOneFileDoesNotKillTheWorkerAndNeverPublishes(t *testing.T) {
	w := newWork(ports.StatusPending)
	_, repo, store := run(t, w, fakeFetcher{data: pngData, mime: "image/png"}, &fakeScanner{panics: true}, DefaultConfig())
	if r := repo.get(w.ID); r.work.Status == ports.StatusClean {
		t.Fatal("a crashed scan must never end clean")
	}
	if len(store.clean) != 0 {
		t.Fatal("nothing may be published after a panic")
	}
}

func TestRetentionRemovesOldFilesAndKeepsTheRow(t *testing.T) {
	old, fresh := newWork(ports.StatusClean), newWork(ports.StatusClean)
	old.CreatedAt = time.Now().Add(-61 * 24 * time.Hour)
	fresh.CreatedAt = time.Now().Add(-59 * 24 * time.Hour)
	repo, store := newRepo(old, fresh), newStore()
	store.clean[old.ID], store.clean[fresh.ID] = pngData, pngData
	p, _ := NewProcessor(repo, store, fakeFetcher{}, &fakeScanner{}, DefaultConfig(), nil)
	n, err := p.PurgeExpired(context.Background())
	if err != nil || n != 1 {
		t.Fatalf("purged %d, err %v", n, err)
	}
	if _, ok := store.clean[old.ID]; ok {
		t.Fatal("a 61-day-old file must be removed")
	}
	if _, ok := store.clean[fresh.ID]; !ok {
		t.Fatal("a 59-day-old file must stay")
	}
	if !repo.get(old.ID).purged || repo.get(fresh.ID).purged {
		t.Fatal("only the old row is marked purged")
	}
}
