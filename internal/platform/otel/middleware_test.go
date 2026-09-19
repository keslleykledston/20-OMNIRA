package otel

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/sdk/trace"
)

func TestHTTPMiddleware_GeneratesCorrelationID(t *testing.T) {
	tp := trace.NewTracerProvider()
	otel.SetTracerProvider(tp)
	tracer := tp.Tracer("test")

	middleware := HTTPMiddleware(tracer)

	handler := middleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Verificar correlation ID no context
		correlationID := GetCorrelationID(r.Context())
		if correlationID == "" {
			t.Error("expected correlation ID in context")
		}

		w.WriteHeader(http.StatusOK)
		w.Write([]byte("ok"))
	}))

	req := httptest.NewRequest("GET", "/test", nil)
	w := httptest.NewRecorder()

	handler.ServeHTTP(w, req)

	// Verificar response header
	correlationID := w.Header().Get(CorrelationIDHeader)
	if correlationID == "" {
		t.Error("expected correlation ID in response header")
	}
}

func TestHTTPMiddleware_PreservesCorrelationID(t *testing.T) {
	tp := trace.NewTracerProvider()
	otel.SetTracerProvider(tp)
	tracer := tp.Tracer("test")

	middleware := HTTPMiddleware(tracer)
	expectedID := "test-correlation-id-123"

	handler := middleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		correlationID := GetCorrelationID(r.Context())
		if correlationID != expectedID {
			t.Errorf("expected correlation ID %s, got %s", expectedID, correlationID)
		}

		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest("GET", "/test", nil)
	req.Header.Set(CorrelationIDHeader, expectedID)
	w := httptest.NewRecorder()

	handler.ServeHTTP(w, req)

	// Verificar que o header foi preservado na resposta
	if w.Header().Get(CorrelationIDHeader) != expectedID {
		t.Errorf("expected correlation ID %s in response, got %s", expectedID, w.Header().Get(CorrelationIDHeader))
	}
}

func TestGetCorrelationID_Missing(t *testing.T) {
	ctx := context.Background()
	correlationID := GetCorrelationID(ctx)
	if correlationID != "" {
		t.Errorf("expected empty correlation ID, got %s", correlationID)
	}
}

func TestWithCorrelationID(t *testing.T) {
	ctx := context.Background()
	expectedID := "test-id"

	ctx = WithCorrelationID(ctx, expectedID)
	correlationID := GetCorrelationID(ctx)

	if correlationID != expectedID {
		t.Errorf("expected correlation ID %s, got %s", expectedID, correlationID)
	}
}

func TestHTTPMiddleware_StatusCode(t *testing.T) {
	tp := trace.NewTracerProvider()
	otel.SetTracerProvider(tp)
	tracer := tp.Tracer("test")

	middleware := HTTPMiddleware(tracer)

	tests := []struct {
		statusCode int
		name       string
	}{
		{http.StatusOK, "200 OK"},
		{http.StatusCreated, "201 Created"},
		{http.StatusNotFound, "404 Not Found"},
		{http.StatusInternalServerError, "500 Internal Server Error"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			handler := middleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(tt.statusCode)
			}))

			req := httptest.NewRequest("GET", "/test", nil)
			w := httptest.NewRecorder()

			handler.ServeHTTP(w, req)

			if w.Code != tt.statusCode {
				t.Errorf("expected status %d, got %d", tt.statusCode, w.Code)
			}
		})
	}
}
