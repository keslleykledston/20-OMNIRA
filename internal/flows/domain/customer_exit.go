package domain

import (
	"fmt"
	"strings"
	"unicode"

	"golang.org/x/text/runes"
	"golang.org/x/text/transform"
	"golang.org/x/text/unicode/norm"
)

// CustomerExit lets the contact end the attendance by typing a command, at any moment the bot is in charge (or the contact
// is still waiting in the queue and nobody has taken the conversation).
//
// It is built to NOT fire by accident:
//  1. the WHOLE message must equal a configured command after normalization (case, accents, outer punctuation and spaces
//     ignored): "não quero encerrar meu contrato" or "preciso sair de casa" never match, there is no substring search;
//  2. a command is at most 3 words, never a bare digit or a yes/no word (those belong to menus and to the confirmation);
//  3. an option of the menu the bot is waiting on that equals the message always wins over the command;
//  4. the bot ALWAYS asks for confirmation and only an explicit "sim" closes; any other answer keeps the attendance open
//     and is handled as a normal reply. There is deliberately no setting to switch the confirmation off.
type CustomerExit struct {
	Enabled bool `json:"enabled"`
	// Commands: whole-message triggers. Empty uses DefaultExitCommands.
	Commands []string `json:"commands,omitempty"`
	// Farewell is sent when the attendance is closed. Empty uses DefaultExitFarewell.
	Farewell string `json:"farewell,omitempty"`
}

var DefaultExitCommands = []string{"encerrar", "encerrar atendimento", "sair", "#sair"}

const (
	DefaultExitFarewell   = "Atendimento encerrado a seu pedido. Se precisar de algo, é só nos escrever por aqui. Obrigado!"
	ExitConfirmQuestion   = "Quer mesmo encerrar este atendimento?"
	ExitConfirmYesTitle   = "Sim, encerrar"
	ExitConfirmNoTitle    = "Não, continuar"
	ExitPendingTTLSeconds = 600
	maxExitCommands       = 10
	maxExitCommandWords   = 3
	maxExitCommandRunes   = 30
)

var accentStripper = transform.Chain(norm.NFD, runes.Remove(runes.In(unicode.Mn)), norm.NFC)

// NormalizeCommand lowercases, removes accents, drops punctuation at the edges (and a leading '#' or '/'), and collapses
// spaces. Inner punctuation is turned into a space so "sim,encerrar" == "sim encerrar".
func NormalizeCommand(s string) string {
	out, _, err := transform.String(accentStripper, strings.ToLower(s))
	if err != nil {
		out = strings.ToLower(s)
	}
	var b strings.Builder
	for _, r := range out {
		switch {
		case unicode.IsLetter(r) || unicode.IsDigit(r):
			b.WriteRune(r)
		default:
			b.WriteRune(' ')
		}
	}
	return strings.Join(strings.Fields(b.String()), " ")
}

func (c CustomerExit) commands() []string {
	if len(c.Commands) == 0 {
		return DefaultExitCommands
	}
	return c.Commands
}

func (c CustomerExit) FarewellText() string {
	if t := strings.TrimSpace(c.Farewell); t != "" {
		return t
	}
	return DefaultExitFarewell
}

// IsCommand reports whether the whole message is one of the commands.
func (c CustomerExit) IsCommand(message string) bool {
	if !c.Enabled {
		return false
	}
	m := NormalizeCommand(message)
	if m == "" {
		return false
	}
	for _, cmd := range c.commands() {
		if m == NormalizeCommand(cmd) {
			return true
		}
	}
	return false
}

var (
	yesAnswers = map[string]bool{"sim": true, "s": true, "1": true, "sim encerrar": true, "confirmo": true, "pode encerrar": true, "sim pode": true}
	noAnswers  = map[string]bool{"nao": true, "n": true, "2": true, "nao continuar": true, "continuar": true}
)

// ExitAnswer classifies the reply to the confirmation question: "yes", "no" or "" (anything else: not an answer).
func ExitAnswer(message string) string {
	m := NormalizeCommand(message)
	switch {
	case yesAnswers[m]:
		return "yes"
	case noAnswers[m]:
		return "no"
	}
	return ""
}

// Validate keeps the commands safe to match against whole messages.
func (c CustomerExit) Validate() error {
	if !c.Enabled {
		return nil
	}
	if len(c.Commands) > maxExitCommands {
		return fmt.Errorf("at most %d exit commands", maxExitCommands)
	}
	if len([]rune(c.Farewell)) > 500 {
		return fmt.Errorf("the farewell is too long (500 characters)")
	}
	for _, raw := range c.commands() {
		n := NormalizeCommand(raw)
		words := strings.Fields(n)
		switch {
		case n == "" || len([]rune(raw)) > maxExitCommandRunes:
			return fmt.Errorf("exit command %q is empty or longer than %d characters", raw, maxExitCommandRunes)
		case len(words) > maxExitCommandWords:
			return fmt.Errorf("exit command %q has more than %d words: a command must be short so ordinary sentences never match", raw, maxExitCommandWords)
		case yesAnswers[n] || noAnswers[n]:
			return fmt.Errorf("exit command %q would collide with the confirmation answers", raw)
		case len(n) == 1 || isDigits(n):
			return fmt.Errorf("exit command %q is too generic (a single character or number)", raw)
		}
	}
	return nil
}

func isDigits(s string) bool {
	for _, r := range s {
		if !unicode.IsDigit(r) && r != ' ' {
			return false
		}
	}
	return s != ""
}
