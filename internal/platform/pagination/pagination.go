package pagination

import (
	"encoding/base64"
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
	encoded := fmt.Sprintf("%s:%d", c.ID, c.Timestamp.Unix())
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

	return &Cursor{
		ID:        id,
		Timestamp: time.Unix(timestamp, 0),
	}, nil
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
