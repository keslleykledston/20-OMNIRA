package pagination

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"time"
)

// Cursor — representação de posição na lista para cursor-based pagination
type Cursor struct {
	ID        string
	Timestamp time.Time
}

// Encode — codifica cursor para string (base64)
func (c *Cursor) Encode() string {
	if c == nil {
		return ""
	}
	encoded := fmt.Sprintf("%s:%d", c.ID, c.Timestamp.UnixNano())
	return base64.StdEncoding.EncodeToString([]byte(encoded))
}

// DecodeCursor — decodifica string para Cursor
func DecodeCursor(s string) (*Cursor, error) {
	if s == "" {
		return nil, nil
	}

	decoded, err := base64.StdEncoding.DecodeString(s)
	if err != nil {
		return nil, fmt.Errorf("invalid cursor: %w", err)
	}

	parts := strings.Split(string(decoded), ":")
	if len(parts) != 2 {
		return nil, fmt.Errorf("invalid cursor format: expected 'id:timestamp'")
	}

	id := parts[0]
	timestamp, err := strconv.ParseInt(parts[1], 10, 64)
	if err != nil {
		return nil, fmt.Errorf("invalid cursor timestamp: %w", err)
	}

	if timestamp < 1_000_000_000_000 {
		return &Cursor{ID: id, Timestamp: time.Unix(timestamp, 0)}, nil
	}
	return &Cursor{ID: id, Timestamp: time.Unix(0, timestamp)}, nil
}

// PageOptions — opções para paginação
type PageOptions struct {
	Limit  int    // Número de items por página (default 20, max 100)
	Cursor string // Cursor para próxima página
	Sort   string // Campo para ordenação (ex: "created_at:asc" ou "created_at:desc")
}

// PageResult — resultado de uma página
type PageResult struct {
	Items      []interface{} `json:"items"`
	NextCursor string        `json:"next_cursor,omitempty"`
	HasMore    bool          `json:"has_more"`
	Count      int           `json:"count"`
	Limit      int           `json:"limit"`
	// Total is how many rows match the filters in all pages (set by the lists that show a result counter; absent elsewhere).
	Total *int `json:"total,omitempty"`
}

// NewPageResult — cria novo resultado de página
func NewPageResult(items []interface{}, limit int, nextCursor string, hasMore bool) *PageResult {
	return &PageResult{
		Items:      items,
		NextCursor: nextCursor,
		HasMore:    hasMore,
		Count:      len(items),
		Limit:      limit,
	}
}

// Filter — representa um filtro de query
type Filter struct {
	Field    string
	Operator string // eq, ne, gt, gte, lt, lte, in, contains
	Value    interface{}
}

// SortDirection — direção de ordenação
type SortDirection string

const (
	SortAsc  SortDirection = "asc"
	SortDesc SortDirection = "desc"
)

// SortSpec — especificação de ordenação
type SortSpec struct {
	Field     string
	Direction SortDirection
}

// ParseSort — analisa string de sort (formato: "field:asc" ou "field:desc")
func ParseSort(s string) (*SortSpec, error) {
	if s == "" {
		return nil, nil
	}

	parts := strings.Split(s, ":")
	if len(parts) != 2 {
		return nil, fmt.Errorf("invalid sort format (expected 'field:asc|desc')")
	}

	field := parts[0]
	dirStr := parts[1]

	dir := SortDirection(dirStr)
	if dir != SortAsc && dir != SortDesc {
		return nil, fmt.Errorf("invalid sort direction: %s", dirStr)
	}

	return &SortSpec{
		Field:     field,
		Direction: dir,
	}, nil
}

// KeyCursor is the position in a list ordered by something other than the default activity time: the value the list is ordered by (Key, as text) and the row id that breaks
// ties. It names the sort it belongs to, so a cursor taken under one ordering is refused under another instead of silently skipping or repeating rows.
type KeyCursor struct {
	Sort string `json:"s"`
	Key  string `json:"k"`
	ID   string `json:"i"`
}

const keyCursorPrefix = "k1."

// Encode — string opaca para a próxima página.
func (c *KeyCursor) Encode() string {
	if c == nil {
		return ""
	}
	raw, _ := json.Marshal(c)
	return keyCursorPrefix + base64.RawURLEncoding.EncodeToString(raw)
}

// DecodeKeyCursor reads a cursor made by KeyCursor.Encode for THIS sort. Empty means the first page. A cursor of another sort, or the default time cursor, is an error.
func DecodeKeyCursor(s, sort string) (*KeyCursor, error) {
	if s == "" {
		return nil, nil
	}
	if !strings.HasPrefix(s, keyCursorPrefix) {
		return nil, fmt.Errorf("invalid cursor for this sort")
	}
	raw, err := base64.RawURLEncoding.DecodeString(strings.TrimPrefix(s, keyCursorPrefix))
	if err != nil {
		return nil, fmt.Errorf("invalid cursor: %w", err)
	}
	var c KeyCursor
	if err := json.Unmarshal(raw, &c); err != nil || c.ID == "" || c.Sort != sort {
		return nil, fmt.Errorf("invalid cursor for this sort")
	}
	return &c, nil
}
