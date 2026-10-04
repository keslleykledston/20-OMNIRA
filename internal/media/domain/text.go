package domain

import "github.com/omnira/omnira/internal/platform/untrusted"

// The implementation moved to internal/platform/untrusted so that media and intelligence share one version of these rules.

// MaxDerivedTextRunes bounds the text stored per attachment (mirrors the column CHECK).
const MaxDerivedTextRunes = untrusted.MaxDerivedTextRunes

func SanitizeDerivedText(in string) string  { return untrusted.SanitizeDerivedText(in) }
func IsStockHallucination(text string) bool { return untrusted.IsStockHallucination(text) }
func LooksLikeInstruction(text string) bool { return untrusted.LooksLikeInstruction(text) }
