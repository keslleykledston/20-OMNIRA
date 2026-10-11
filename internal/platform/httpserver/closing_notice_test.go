package httpserver

import (
	"errors"
	"testing"

	attendancedomain "github.com/omnira/omnira/internal/attendance/domain"
	messagesapplication "github.com/omnira/omnira/internal/messages/application"
)

// What a finalize does with the answer of the sender when it queues the closing notice (ADR-0040 phase 08): a clear refusal of the courtesy becomes a state and the
// finalize goes on; anything else is an error, which aborts the whole finalize (nothing closed, nothing queued).
func TestClosingNoticeStateMapsOnlyClearRefusalsAndPropagatesTheRest(t *testing.T) {
	ok := messagesapplication.SendResult{}
	for name, tc := range map[string]struct {
		err  error
		want attendancedomain.FarewellState
	}{
		"queued":                    {nil, attendancedomain.FarewellQueued},
		"closed window":             {messagesapplication.ErrWindowClosed, attendancedomain.FarewellWindowClosed},
		"no line":                   {messagesapplication.ErrChannelUnavailable, attendancedomain.FarewellNoChannel},
		"forbidden":                 {messagesapplication.ErrForbidden, attendancedomain.FarewellNotAllowed},
		"unassigned":                {messagesapplication.ErrUnassigned, attendancedomain.FarewellNotAllowed},
		"assigned to somebody else": {messagesapplication.ErrNotAssignedToYou, attendancedomain.FarewellNotAllowed},
	} {
		got, err := closingNoticeState(ok, tc.err)
		if err != nil || got != tc.want {
			t.Errorf("%s: state %q err %v, want %q", name, got, err, tc.want)
		}
	}
	for name, e := range map[string]error{
		"an unknown error":        errors.New("boom"),
		"an idempotency mismatch": messagesapplication.ErrIdempotencyMismatch,
		"a changed conversation":  messagesapplication.ErrConversationChanged,
		"a closed conversation":   messagesapplication.ErrConversationClosed,
		"an invalid text":         messagesapplication.ErrInvalidText,
	} {
		if got, err := closingNoticeState(ok, e); err == nil || got != "" {
			t.Errorf("%s: state %q err %v, want the error propagated and no state", name, got, err)
		}
	}
}

func TestNoticeKeyTaken(t *testing.T) {
	if st, err := noticeKeyTaken(true, nil); err != nil || st != attendancedomain.FarewellUnavailable {
		t.Errorf("a taken key must not claim the notice is queued: %q %v", st, err)
	}
	if _, err := noticeKeyTaken(false, errors.New("db")); err == nil {
		t.Error("an error must stay an error")
	}
	if st, err := noticeKeyTaken(true, errors.New("db")); err == nil || st != "" {
		t.Error("an error wins over a taken key")
	}
}
