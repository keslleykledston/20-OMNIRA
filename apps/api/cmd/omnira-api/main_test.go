package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/omnira/omnira/internal/platform/httpserver"
)

func TestHealthLiveEndpoint(t *testing.T) {
	srv := httpserver.New(":0")
	srv.RegisterHealthHandlers()

	mux := http.NewServeMux()
	mux.HandleFunc("GET /internal/health/live", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"status":"ok"}`))
	})

	rec := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/internal/health/live", nil)
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Errorf("expected status 200, got %d", rec.Code)
	}

	if rec.Header().Get("Content-Type") != "application/json" {
		t.Errorf("expected Content-Type application/json, got %s", rec.Header().Get("Content-Type"))
	}
}

func TestHealthReadyEndpoint(t *testing.T) {
	srv := httpserver.New(":0")
	srv.RegisterHealthHandlers()

	mux := http.NewServeMux()
	mux.HandleFunc("GET /internal/health/ready", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"status":"ready"}`))
	})

	rec := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/internal/health/ready", nil)
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Errorf("expected status 200, got %d", rec.Code)
	}
}

func TestGracefulShutdown(t *testing.T) {
	srv := httpserver.New(":0")

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	errChan := make(chan error, 1)
	go func() {
		errChan <- srv.ListenAndServe(ctx)
	}()

	time.Sleep(100 * time.Millisecond)

	if err := srv.Shutdown(); err != nil {
		t.Errorf("shutdown error: %v", err)
	}

	select {
	case <-time.After(2 * time.Second):
		t.Error("shutdown timeout")
	case <-srv.Done():
	}
}
