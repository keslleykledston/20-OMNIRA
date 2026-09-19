package pagination

import (
	"net/http"
	"strconv"
)

// ParsePageOptionsFromQuery — extrai opções de paginação da query string
func ParsePageOptionsFromQuery(r *http.Request) *PageOptions {
	opts := &PageOptions{
		Limit:  20, // default
		Cursor: r.URL.Query().Get("cursor"),
		Sort:   r.URL.Query().Get("sort"),
	}

	// Parse limit
	if limitStr := r.URL.Query().Get("limit"); limitStr != "" {
		if limit, err := strconv.Atoi(limitStr); err == nil && limit > 0 {
			if limit > 100 {
				limit = 100 // max
			}
			opts.Limit = limit
		}
	}

	return opts
}

// WritePaginationHeaders — escreve headers de paginação na resposta
func WritePaginationHeaders(w http.ResponseWriter, result *PageResult) {
	w.Header().Set("X-Pagination-Count", strconv.Itoa(result.Count))
	w.Header().Set("X-Pagination-Limit", strconv.Itoa(result.Limit))
	w.Header().Set("X-Pagination-HasMore", strconv.FormatBool(result.HasMore))
	if result.NextCursor != "" {
		w.Header().Set("X-Pagination-NextCursor", result.NextCursor)
	}
}
