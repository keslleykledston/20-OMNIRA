package pagination

import (
	"strconv"
	"testing"
	"time"
)

func TestCursorEncodeDecode(t *testing.T) {
	// Create cursor
	ts := time.Unix(1695312345, 0)
	cursor := &Cursor{
		ID:        "user-123",
		Timestamp: ts,
	}

	// Encode
	encoded := cursor.Encode()
	if encoded == "" {
		t.Errorf("encoded cursor should not be empty")
	}

	// Decode
	decoded, err := DecodeCursor(encoded)
	if err != nil {
		t.Errorf("decode error: %v", err)
	}

	if decoded.ID != cursor.ID {
		t.Errorf("ID mismatch: %s != %s", decoded.ID, cursor.ID)
	}

	if decoded.Timestamp != cursor.Timestamp {
		t.Errorf("timestamp mismatch: %v != %v", decoded.Timestamp, cursor.Timestamp)
	}
}

func TestCursorNil(t *testing.T) {
	var cursor *Cursor
	encoded := cursor.Encode()
	if encoded != "" {
		t.Errorf("nil cursor should encode to empty string, got %s", encoded)
	}

	decoded, err := DecodeCursor("")
	if err != nil {
		t.Errorf("decode empty string should not error, got %v", err)
	}

	if decoded != nil {
		t.Errorf("empty cursor should decode to nil")
	}
}

func TestCursorInvalidEncode(t *testing.T) {
	_, err := DecodeCursor("invalid-base64!!!")
	if err == nil {
		t.Errorf("invalid base64 should error")
	}
}

func TestParseSort(t *testing.T) {
	tests := []struct {
		input     string
		expectErr bool
		field     string
		direction SortDirection
	}{
		{"created_at:asc", false, "created_at", SortAsc},
		{"name:desc", false, "name", SortDesc},
		{"", false, "", ""},
		{"invalid", true, "", ""},
		{"field:invalid", true, "", ""},
	}

	for _, test := range tests {
		spec, err := ParseSort(test.input)
		if test.expectErr && err == nil {
			t.Errorf("ParseSort(%q): expected error, got nil", test.input)
		}
		if !test.expectErr && err != nil {
			t.Errorf("ParseSort(%q): unexpected error: %v", test.input, err)
		}
		if !test.expectErr && spec != nil {
			if spec.Field != test.field || spec.Direction != test.direction {
				t.Errorf("ParseSort(%q): expected %s:%s, got %s:%s", test.input, test.field, test.direction, spec.Field, spec.Direction)
			}
		}
	}
}

func TestPageResult(t *testing.T) {
	items := []interface{}{"item1", "item2", "item3"}
	result := NewPageResult(items, 20, "next-cursor", true)

	if result.Count != 3 {
		t.Errorf("count: expected 3, got %d", result.Count)
	}

	if result.Limit != 20 {
		t.Errorf("limit: expected 20, got %d", result.Limit)
	}

	if result.NextCursor != "next-cursor" {
		t.Errorf("next_cursor: expected 'next-cursor', got %s", result.NextCursor)
	}

	if !result.HasMore {
		t.Errorf("has_more: expected true, got false")
	}
}

func TestKeyCursorRoundTripsAndRefusesAnotherSort(t *testing.T) {
	c := &KeyCursor{Sort: "name:asc", Key: "ana: souza | \"x\"", ID: "11111111-1111-1111-1111-111111111111"}
	got, err := DecodeKeyCursor(c.Encode(), "name:asc")
	if err != nil || got == nil || *got != *c {
		t.Fatalf("round trip = %+v, %v", got, err)
	}
	if _, err := DecodeKeyCursor(c.Encode(), "name:desc"); err == nil {
		t.Error("a cursor of another sort must be refused")
	}
	if got, err := DecodeKeyCursor("", "name:asc"); got != nil || err != nil {
		t.Errorf("empty = %+v, %v; want the first page", got, err)
	}
	// the default time cursor and garbage are not key cursors
	if _, err := DecodeKeyCursor((&Cursor{ID: "x", Timestamp: time.Now()}).Encode(), "name:asc"); err == nil {
		t.Error("a time cursor must not pass as a key cursor")
	}
	if _, err := DecodeKeyCursor("k1.%%%", "name:asc"); err == nil {
		t.Error("garbage must be refused")
	}
}

func TestKeysetWhereBindsTheTypedKeyAndTheDirection(t *testing.T) {
	var args []any
	arg := func(v any) string { args = append(args, v); return "$" + strconv.Itoa(len(args)) }
	id := "22222222-2222-2222-2222-222222222222"

	when := time.Date(2026, 10, 18, 12, 0, 0, 123456000, time.UTC)
	txt, err := (SortColumn{Expr: "updated_at", Kind: SortTime}).KeyText(when)
	if err != nil {
		t.Fatal(err)
	}
	where, err := KeysetWhere(SortColumn{Expr: "updated_at", Kind: SortTime}, "id", false, &KeyCursor{Key: txt, ID: id}, arg)
	if err != nil || where != "(updated_at, id) > ($1, $2)" {
		t.Fatalf("asc time = %q, %v", where, err)
	}
	if got, ok := args[0].(time.Time); !ok || !got.Equal(when) {
		t.Errorf("the time key lost precision: %v", args[0])
	}

	args = nil
	where, err = KeysetWhere(SortColumn{Expr: "lower(subject)", Kind: SortText}, "id", true, &KeyCursor{Key: "ana", ID: id}, arg)
	if err != nil || where != "(lower(subject), id) < ($1, $2)" || args[0] != "ana" {
		t.Fatalf("desc text = %q %v, %v", where, args, err)
	}

	args = nil
	if _, err := KeysetWhere(SortColumn{Expr: "rank", Kind: SortInt}, "id", false, &KeyCursor{Key: "abc", ID: id}, arg); err == nil {
		t.Error("a non-numeric int key must be refused")
	}
	if _, err := KeysetWhere(SortColumn{Expr: "x", Kind: SortText}, "id", false, &KeyCursor{Key: "a", ID: "not-a-uuid"}, arg); err == nil {
		t.Error("a bad id must be refused")
	}
	if _, err := (SortColumn{Expr: "x", Kind: SortTime}).KeyText("text"); err == nil {
		t.Error("a value that does not fit the column must be refused")
	}
}
