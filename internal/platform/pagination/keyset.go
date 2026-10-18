package pagination

import (
	"fmt"
	"strconv"
	"time"

	"github.com/google/uuid"
)

// SortKind is the type of the value a list is ordered by, which decides how the cursor carries it.
type SortKind int

const (
	SortTime SortKind = iota
	SortText
	SortInt
)

// SortColumn is one thing a list may be ordered by: the SQL expression (written by the list, never by the caller) and its type.
type SortColumn struct {
	Expr string
	Kind SortKind
}

// KeyText turns the value the database returned for Expr into the text the cursor carries.
func (c SortColumn) KeyText(v any) (string, error) {
	switch x := v.(type) {
	case time.Time:
		if c.Kind == SortTime {
			return x.UTC().Format(time.RFC3339Nano), nil
		}
	case string:
		if c.Kind == SortText {
			return x, nil
		}
	case int16:
		return strconv.FormatInt(int64(x), 10), nil
	case int32:
		return strconv.FormatInt(int64(x), 10), nil
	case int64:
		return strconv.FormatInt(x, 10), nil
	}
	return "", fmt.Errorf("pagination: sort value of type %T does not fit the column", v)
}

// param is the inverse of KeyText: the cursor's text as a query argument of the column's type.
func (c SortColumn) param(key string) (any, error) {
	switch c.Kind {
	case SortTime:
		t, err := time.Parse(time.RFC3339Nano, key)
		return t, err
	case SortInt:
		n, err := strconv.ParseInt(key, 10, 64)
		return n, err
	default:
		return key, nil
	}
}

// KeysetWhere is the predicate that continues the list after the cursor's row: rows strictly after (Expr, id) in the list's order. `arg` binds a value and returns its
// placeholder, like the list handlers already do. A cursor whose key or id does not parse is an error (the caller answers 400).
func KeysetWhere(col SortColumn, idExpr string, desc bool, c *KeyCursor, arg func(any) string) (string, error) {
	v, err := col.param(c.Key)
	if err != nil {
		return "", fmt.Errorf("invalid cursor: %w", err)
	}
	id, err := uuid.Parse(c.ID)
	if err != nil {
		return "", fmt.Errorf("invalid cursor: %w", err)
	}
	op := ">"
	if desc {
		op = "<"
	}
	return "(" + col.Expr + ", " + idExpr + ") " + op + " (" + arg(v) + ", " + arg(id) + ")", nil
}
