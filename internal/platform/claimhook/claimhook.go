// Package claimhook is the way "taking a conversation" (the self-claim of a member, the claim of a Hub agent attending an instance) tells an optional module
// that someone assumed it. The routes are built before the optional modules are wired, so they all hold the same *Hook and the module installs its function
// on it later. An empty hook does nothing: assuming a conversation never depends on the module.
package claimhook

import (
	"context"
	"sync/atomic"

	"github.com/google/uuid"
)

// Func runs in the transaction of the claim (or right after it, in the Hub's write path). It reports whether it changed anything. A failure must never undo
// the claim: Run swallows the error for the caller's convenience through RunLogged, or returns it so the caller can log it with its own ids.
type Func func(ctx context.Context, conversationID, actor uuid.UUID) (created bool, err error)

type Hook struct{ fn atomic.Pointer[Func] }

// Set installs the function (nil removes it).
func (h *Hook) Set(f Func) {
	if h == nil {
		return
	}
	if f == nil {
		h.fn.Store(nil)
		return
	}
	h.fn.Store(&f)
}

// Installed says whether a function is set.
func (h *Hook) Installed() bool { return h != nil && h.fn.Load() != nil }

// Run calls the installed function; (false, nil) when none is.
func (h *Hook) Run(ctx context.Context, conversationID, actor uuid.UUID) (bool, error) {
	if h == nil {
		return false, nil
	}
	f := h.fn.Load()
	if f == nil {
		return false, nil
	}
	return (*f)(ctx, conversationID, actor)
}
