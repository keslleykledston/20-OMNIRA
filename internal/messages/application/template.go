package application

import (
	"regexp"
	"strconv"
	"strings"
	"unicode/utf8"
)

// MaxTemplateParamRunes is conservative: Meta rejects very long body variables.
const MaxTemplateParamRunes = 1024

var placeholder = regexp.MustCompile(`\{\{([0-9]{1,2})\}\}`)

// RenderTemplate validates the variables and returns the template body with them filled in (the text the inbox shows).
// Meta's rules for a body variable: not empty, no line breaks or tabs, and not long runs of spaces. The count must be
// exactly the number of placeholders the template declares.
func RenderTemplate(body string, variableCount int, params []string) (string, error) {
	if len(params) != variableCount {
		return "", ErrTemplateParams
	}
	for _, p := range params {
		if strings.TrimSpace(p) == "" || utf8.RuneCountInString(p) > MaxTemplateParamRunes || strings.ContainsAny(p, "\n\r\t") || strings.Contains(p, "    ") {
			return "", ErrTemplateParams
		}
	}
	out := placeholder.ReplaceAllStringFunc(body, func(m string) string {
		n, _ := strconv.Atoi(placeholder.FindStringSubmatch(m)[1])
		if n >= 1 && n <= len(params) {
			return strings.TrimSpace(params[n-1])
		}
		return m
	})
	if utf8.RuneCountInString(out) > MaxTextRunes {
		return "", ErrTemplateParams
	}
	return out, nil
}
