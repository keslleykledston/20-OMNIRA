package domain

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
)

// decodeModelJSON reads the FIRST JSON object of a model's answer into v.
//
// A language model does not always answer with the bare object it was asked for: it may wrap it in a code fence, add a sentence before or after it, or
// add a field nobody asked for. None of that makes the object itself wrong - every operation is validated field by field afterwards, and a field the
// code does not know is never applied - so it is tolerated. What is not tolerated: no object at all, an answer cut off before the object closes, or an
// object of the wrong shape.
//
// The error says WHAT was wrong, never WHAT the model wrote: the model's text carries the group's messages, and an error ends up in a log.
func decodeModelJSON(raw string, v any) error {
	s := strings.TrimSpace(raw)
	if s == "" {
		return fmt.Errorf("%w: empty answer", ErrInvalidOutput)
	}
	start := strings.IndexByte(s, '{')
	if start < 0 {
		return fmt.Errorf("%w: no JSON object in the answer (%d bytes)", ErrInvalidOutput, len(s))
	}
	if err := json.NewDecoder(strings.NewReader(s[start:])).Decode(v); err != nil {
		return fmt.Errorf("%w: %s", ErrInvalidOutput, describeJSONError(err, len(s)))
	}
	return nil
}

// describeJSONError turns a decoder error into a sentence that holds no content of the answer.
func describeJSONError(err error, size int) string {
	var syn *json.SyntaxError
	var typ *json.UnmarshalTypeError
	switch {
	case errors.Is(err, io.ErrUnexpectedEOF), errors.Is(err, io.EOF):
		return fmt.Sprintf("the answer was cut off before the JSON object closed (%d bytes)", size)
	case errors.As(err, &syn):
		return fmt.Sprintf("JSON syntax error at byte %d of %d", syn.Offset, size)
	case errors.As(err, &typ):
		return fmt.Sprintf("a field has the wrong type (%s, expected %s)", typ.Field, typ.Type)
	}
	return "the JSON object could not be read"
}
