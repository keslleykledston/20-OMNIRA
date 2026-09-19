package pagination

import (
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
