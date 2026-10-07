package domain

import "testing"

func TestExitCommandMatchesOnlyTheWholeMessage(t *testing.T) {
	c := CustomerExit{Enabled: true}
	for _, yes := range []string{"encerrar", "Encerrar", "  ENCERRAR!  ", "Encerrar atendimento.", "#sair", "sair", "sàir"} {
		if !c.IsCommand(yes) {
			t.Errorf("%q must be the command", yes)
		}
	}
	for _, no := range []string{
		"", "oi", "1", "sim", "não quero encerrar meu contrato", "preciso sair de casa às 18h", "encerrar o contrato", "quero sair do plano",
		"encerrar atendimento por favor agora", "sairei", "nao encerrar",
	} {
		if c.IsCommand(no) {
			t.Errorf("%q must NOT be treated as the command (whole-message match only)", no)
		}
	}
	if (CustomerExit{Enabled: false}).IsCommand("encerrar") {
		t.Error("disabled means disabled")
	}
	custom := CustomerExit{Enabled: true, Commands: []string{"tchau"}}
	if !custom.IsCommand("Tchau!") || custom.IsCommand("encerrar") {
		t.Error("configured commands replace the defaults")
	}
}

func TestExitAnswerIsStrict(t *testing.T) {
	for in, want := range map[string]string{"sim": "yes", "SIM!": "yes", "Sim, encerrar": "yes", "1": "yes", "não": "no", "Nao": "no", "Não, continuar": "no", "2": "no",
		"": "", "talvez": "", "sim mas antes me diga o valor": "", "3": ""} {
		if got := ExitAnswer(in); got != want {
			t.Errorf("ExitAnswer(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestExitCommandsAreValidatedSoTheyStaySafe(t *testing.T) {
	bad := map[string]CustomerExit{
		"too many words":    {Enabled: true, Commands: []string{"quero encerrar este atendimento agora"}},
		"a bare digit":      {Enabled: true, Commands: []string{"1"}},
		"collides with yes": {Enabled: true, Commands: []string{"sim"}},
		"collides with no":  {Enabled: true, Commands: []string{"Não"}},
		"single character":  {Enabled: true, Commands: []string{"x"}},
		"empty":             {Enabled: true, Commands: []string{"  !! "}},
		"too many":          {Enabled: true, Commands: []string{"a1", "a2", "a3", "a4", "a5", "a6", "a7", "a8", "a9", "a10", "a11"}},
		"huge farewell":     {Enabled: true, Farewell: string(make([]rune, 501))},
	}
	for name, c := range bad {
		if err := c.Validate(); err == nil {
			t.Errorf("%s must be refused", name)
		}
	}
	if err := (CustomerExit{Enabled: true}).Validate(); err != nil {
		t.Errorf("the defaults must be valid: %v", err)
	}
	if err := (CustomerExit{Enabled: false, Commands: []string{"1"}}).Validate(); err != nil {
		t.Errorf("a disabled block is not judged: %v", err)
	}
}
