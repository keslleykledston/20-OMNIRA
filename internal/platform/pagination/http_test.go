package pagination

import (
	"net/http/httptest"
	"testing"
)

func TestParsePageOptionsFromQuery_Defaults(t *testing.T) {
	req := httptest.NewRequest("GET", "/api/test", nil)
	opts := ParsePageOptionsFromQuery(req)

	if opts.Limit != 20 {
		t.Errorf("default limit: expected 20, got %d", opts.Limit)
	}

	if opts.Cursor != "" {
		t.Errorf("default cursor: expected empty, got %s", opts.Cursor)
	}

	if opts.Sort != "" {
		t.Errorf("default sort: expected empty, got %s", opts.Sort)
	}
}

func TestParsePageOptionsFromQuery_CustomLimit(t *testing.T) {
	req := httptest.NewRequest("GET", "/api/test?limit=50", nil)
	opts := ParsePageOptionsFromQuery(req)

	if opts.Limit != 50 {
		t.Errorf("custom limit: expected 50, got %d", opts.Limit)
	}
}

func TestParsePageOptionsFromQuery_LimitMaxClamp(t *testing.T) {
	req := httptest.NewRequest("GET", "/api/test?limit=500", nil)
	opts := ParsePageOptionsFromQuery(req)

	if opts.Limit != 100 {
		t.Errorf("clamped limit: expected 100, got %d", opts.Limit)
	}
}

func TestParsePageOptionsFromQuery_WithCursorAndSort(t *testing.T) {
	req := httptest.NewRequest("GET", "/api/test?cursor=abc123&sort=created_at:desc", nil)
	opts := ParsePageOptionsFromQuery(req)

	if opts.Cursor != "abc123" {
		t.Errorf("cursor: expected 'abc123', got %s", opts.Cursor)
	}

	if opts.Sort != "created_at:desc" {
		t.Errorf("sort: expected 'created_at:desc', got %s", opts.Sort)
	}
}

func TestParsePageOptionsFromQuery_InvalidLimit(t *testing.T) {
	req := httptest.NewRequest("GET", "/api/test?limit=invalid", nil)
	opts := ParsePageOptionsFromQuery(req)

	if opts.Limit != 20 {
		t.Errorf("invalid limit should use default: expected 20, got %d", opts.Limit)
	}
}

func TestWritePaginationHeaders(t *testing.T) {
	w := httptest.NewRecorder()
	result := NewPageResult([]interface{}{"a", "b"}, 50, "next-cursor-123", true)

	WritePaginationHeaders(w, result)

	// Check headers
	if w.Header().Get("X-Pagination-Count") != "2" {
		t.Errorf("X-Pagination-Count: expected '2', got '%s'", w.Header().Get("X-Pagination-Count"))
	}

	if w.Header().Get("X-Pagination-Limit") != "50" {
		t.Errorf("X-Pagination-Limit: expected '50', got '%s'", w.Header().Get("X-Pagination-Limit"))
	}

	if w.Header().Get("X-Pagination-HasMore") != "true" {
		t.Errorf("X-Pagination-HasMore: expected 'true', got '%s'", w.Header().Get("X-Pagination-HasMore"))
	}

	if w.Header().Get("X-Pagination-NextCursor") != "next-cursor-123" {
		t.Errorf("X-Pagination-NextCursor: expected 'next-cursor-123', got '%s'", w.Header().Get("X-Pagination-NextCursor"))
	}
}

func TestWritePaginationHeaders_NoNextCursor(t *testing.T) {
	w := httptest.NewRecorder()
	result := NewPageResult([]interface{}{}, 50, "", false)

	WritePaginationHeaders(w, result)

	if w.Header().Get("X-Pagination-NextCursor") != "" {
		t.Errorf("X-Pagination-NextCursor should be empty when no next cursor")
	}
}
