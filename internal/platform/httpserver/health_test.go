package httpserver

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// Readiness must reflect the dependencies at call time, not the last /healthz run.
func TestReadinessProbesDependenciesOnEveryCall(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	// Lazy pool pointing at a port nobody listens on: every ping fails.
	pool, err := pgxpool.New(ctx, "postgres://x:x@127.0.0.1:1/none?sslmode=disable&connect_timeout=1")
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	s := New("127.0.0.1:0")
	s.SetupHealth(pool, nil)
	s.RegisterHealthHandlers()

	get := func(path string) (int, string) {
		rec := httptest.NewRecorder()
		s.mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
		return rec.Code, rec.Body.String()
	}
	// No /healthz call happened before: the cache is empty, yet readiness must not claim "ready".
	if code, body := get("/internal/health/ready"); code != http.StatusServiceUnavailable || !strings.Contains(body, "not ready") {
		t.Fatalf("readiness with the database down: %d %s", code, body)
	}
	if code, _ := get("/internal/health/live"); code != http.StatusOK { // liveness stays independent of dependencies
		t.Fatalf("liveness must not depend on the database: %d", code)
	}
	if _, body := get("/metrics"); !strings.Contains(body, `omnira_health_status{component="database"} 0`) {
		t.Fatalf("metrics must report the database as unhealthy:\n%s", body)
	}
}
