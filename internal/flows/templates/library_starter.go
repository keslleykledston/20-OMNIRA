package templates

import "github.com/omnira/omnira/internal/flows/domain"

var generalCats = []string{"GENERAL", "CUSTOMER_SERVICE"}

func starterTemplates() []*Template {
	return []*Template{tUnknownContact(), tCustomerContext(), tExistingTicket(), tHumanHandoff(), tAfterHours(), tCSAT(), tSmartReception()}
}

// unknown-contact: an unclassified contact is NOT a new customer. The bot only asks who they are; it never creates a company
// (a person classifies the contact later). Timeouts do not dead-end the conversation: the parent flow carries on.
func tUnknownContact() *Template {
	b := NewBuilder().Start().
		Ask("ask_name", "Para começar, qual é o seu nome?", "nome").
		Ask("ask_company", "E qual é o nome da sua empresa?", "empresa_informada").
		Say("thanks", "Obrigado, {{nome}}! Vou registrar seu contato e seguir com o atendimento.").
		End("done", "informational").
		Connect("ask_name", "timeout", "done").Connect("ask_company", "timeout", "done").
		Var("nome", "string", "Nome informado pelo contato").Var("empresa_informada", "string", "Empresa declarada (não cria empresa; a classificação é humana)")
	return mk("unknown-contact", 1, "Contato desconhecido", "Pergunta nome e empresa de quem ainda não foi classificado, sem criar empresa.",
		domain.FlowTypeSubflow, generalCats, Settings{}, b, []TestCase{
			{Name: "coleta nome e empresa", Scenario: Scenario{Events: Msg("oi", "João Silva", "Acme Ltda")},
				Expect: Expect{Status: "completed", Say: []string{"seu nome", "nome da sua empresa", "Obrigado, João Silva"}, Vars: map[string]string{"nome": "João Silva", "empresa_informada": "Acme Ltda"}, NoEffects: true}},
			{Name: "sem resposta não trava a conversa", Scenario: Scenario{Events: []Event{{Text: "oi"}, {Timeout: true}}},
				Expect: Expect{Status: "completed", Reaches: []string{"done"}, NotReaches: []string{"thanks"}}},
		}, recommended("general", "isp", "msp"))
}

// customer-context: 0/1 company continue; several companies are NEVER auto-selected: the contact chooses.
func tCustomerContext() *Template {
	b := NewBuilder().Start().Company("company").
		From("company", "none").End("done", "informational").
		Connect("company", "single", "done").
		From("company", "multiple").CompanyChoice("pick", "Sobre qual empresa é este atendimento?").
		Connect("pick", "selected", "done").
		Handoff("h_fallback", "queue.fallback", "Cliente com várias empresas não escolheu a empresa do atendimento.").
		Connect("pick", "timeout", "h_fallback")
	return mk("customer-context", 1, "Contexto da empresa", "Descobre de qual empresa é o atendimento; com várias empresas, pergunta ao contato (nunca escolhe a primeira).",
		domain.FlowTypeSubflow, generalCats, Settings{}, b, []TestCase{
			{Name: "sem empresa", Scenario: Scenario{ContactKind: "customer", Events: Msg("oi")}, Expect: Expect{Status: "completed", Reaches: []string{"done"}, NoMessages: true}},
			{Name: "uma empresa é selecionada sozinha", Scenario: Scenario{ContactKind: "customer", Companies: []string{"ACME"}, Events: Msg("oi")},
				Expect: Expect{Status: "completed", Effects: []string{"company_validated"}, NoMessages: true}},
			{Name: "várias empresas: pergunta e espera", Scenario: Scenario{ContactKind: "customer", Companies: []string{"ACME", "Beta"}, Events: Msg("oi")},
				Expect: Expect{Status: "waiting_input", WaitingAt: "pick", Say: []string{"1) ACME", "2) Beta"}, NoEffects: true}},
			{Name: "várias empresas: escolhe a segunda", Scenario: Scenario{ContactKind: "customer", Companies: []string{"ACME", "Beta"}, Events: Msg("oi", "2")},
				Expect: Expect{Status: "completed", Effects: []string{"company_validated"}}},
			{Name: "várias empresas e sem resposta: humano", Scenario: Scenario{ContactKind: "customer", Companies: []string{"ACME", "Beta"}, Events: []Event{{Text: "oi"}, {Timeout: true}}},
				Expect: Expect{Status: "waiting_human", Effects: []string{"handoff"}}},
		}, recommended("general", "isp", "msp"))
}

// existing-ticket: asks whether the contact is talking about a ticket they already have; sets related=sim|nao.
func tExistingTicket() *Template {
	b := NewBuilder().Start().FindTickets("find").
		From("find", "none").Set("not_related", "related", "nao").End("done", "informational").
		From("find", "found").
		Choice("ask", "Você já tem um chamado aberto: \"{{tickets.first_subject}}\". Seu assunto é sobre ele?", "relacionado",
			Opt{"sim", "Sim, é sobre esse chamado", ""}, Opt{"nao", "Não, é outro assunto", ""}).
		From("ask", "sim").Set("yes", "related", "sim").Connect("yes", "next", "done").
		Connect("ask", "nao", "not_related").Connect("ask", "timeout", "not_related").
		Var("related", "string", "sim|nao: o contato fala de um chamado que já existe").Var("relacionado", "string", "Resposta do contato")
	return mk("existing-ticket", 1, "Chamado existente", "Evita chamado duplicado: pergunta se o assunto é de um chamado aberto e devolve related=sim|nao.",
		domain.FlowTypeSubflow, generalCats, Settings{}, b, []TestCase{
			{Name: "sem chamado aberto", Scenario: Scenario{Events: Msg("oi")}, Expect: Expect{Status: "completed", Vars: map[string]string{"related": "nao"}, NoMessages: true}},
			{Name: "tem chamado e é sobre ele", Scenario: Scenario{OpenTickets: 1, Events: Msg("oi", "1")}, Expect: Expect{Status: "completed", Vars: map[string]string{"related": "sim"}}},
			{Name: "tem chamado mas é outro assunto", Scenario: Scenario{OpenTickets: 1, Events: Msg("oi", "outro")}, Expect: Expect{Status: "waiting_input"}},
			{Name: "tem chamado e escolhe 2", Scenario: Scenario{OpenTickets: 1, Events: Msg("oi", "2")}, Expect: Expect{Status: "completed", Vars: map[string]string{"related": "nao"}}},
			{Name: "sem resposta assume outro assunto", Scenario: Scenario{OpenTickets: 1, Events: []Event{{Text: "oi"}, {Timeout: true}}}, Expect: Expect{Status: "completed", Vars: map[string]string{"related": "nao"}}},
		}, recommended("general", "isp", "msp"))
}

func tHumanHandoff() *Template {
	b := NewBuilder().Start().Say("say", "Vou transferir você para um atendente. Um momento, por favor.").
		Handoff("handoff", "queue.fallback", "Transferido pelo bot a pedido do fluxo. Contato: {{contact.name}}.")
	return mk("human-handoff", 1, "Transferir para humano", "Avisa o contato e entrega a conversa à fila de triagem humana.",
		domain.FlowTypeSubflow, generalCats, Settings{}, b, []TestCase{
			{Name: "avisa e transfere", Scenario: Scenario{Events: Msg("oi")}, Expect: Expect{Status: "waiting_human", Say: []string{"transferir"}, Effects: []string{"handoff"}}},
		}, recommended("general", "isp", "msp"))
}

func tAfterHours() *Template {
	b := NewBuilder().Start().
		Say("say", "No momento estamos fora do horário de atendimento. Registramos sua mensagem e retornaremos no próximo horário útil.").
		Ticket("ticket", "Fora do horário - {{contact.name}}", "medium").End("done", "informational")
	return mk("after-hours", 1, "Fora do horário", "Mensagem de fora do horário e registro de um chamado para o próximo expediente.",
		domain.FlowTypeSubflow, []string{"GENERAL", "AFTER_HOURS"}, Settings{}, b, []TestCase{
			{Name: "avisa e registra chamado", Scenario: Scenario{Events: Msg("oi")}, Expect: Expect{Status: "completed", Say: []string{"fora do horário"}, Effects: []string{"ticket"}, Priority: "medium"}},
		}, recommended("general", "isp", "msp"))
}

func tCSAT() *Template {
	b := NewBuilder().Start().
		Choice("score", "De 1 a 5, como você avalia o atendimento?", "csat_nota",
			Opt{"n1", "1", ""}, Opt{"n2", "2", ""}, Opt{"n3", "3", ""}, Opt{"n4", "4", ""}, Opt{"n5", "5", ""}).
		From("score", "n1").Say("thanks", "Obrigado pela avaliação!").End("done", "resolved").
		Connect("score", "n2", "thanks").Connect("score", "n3", "thanks").Connect("score", "n4", "thanks").Connect("score", "n5", "thanks").
		Connect("score", "timeout", "done").
		Var("csat_nota", "string", "Nota de 1 a 5")
	return mk("csat", 1, "Pesquisa de satisfação", "Pergunta a nota de 1 a 5 e agradece. Chame-o como subflow ao fim de um atendimento.",
		domain.FlowTypeSubflow, []string{"GENERAL", "SURVEY"}, Settings{}, b, []TestCase{
			{Name: "nota 5", Scenario: Scenario{Events: Msg("oi", "5")}, Expect: Expect{Status: "completed", Say: []string{"1) 1", "Obrigado pela avaliação"}, Vars: map[string]string{"csat_nota": "5"}}},
			{Name: "sem resposta encerra sem agradecer", Scenario: Scenario{Events: []Event{{Text: "oi"}, {Timeout: true}}}, Expect: Expect{Status: "completed", NotReaches: []string{"thanks"}}},
		}, recommended("general", "isp", "msp"))
}

func tSmartReception() *Template {
	b := NewBuilder().Start().
		Say("hello", "Olá {{contact.name}}! Bem-vindo ao atendimento.").
		Contact("who").
		From("who", "known").Sub("ctx", "customer-context").
		Choice("menu", "Como podemos ajudar?", "assunto",
			Opt{"tech", "Suporte técnico", ""}, Opt{"fin", "Financeiro", ""}, Opt{"com", "Comercial", ""}, Opt{"outro", "Outro assunto", ""}).
		From("who", "unknown").Sub("unk", "unknown-contact").Connect("unk", "next", "menu").
		From("menu", "tech").Sub("exist", "existing-ticket").
		Condition("related", "related", "eq", "sim").
		From("related", "true").Handoff("h_exist", "queue.technical", "Cliente {{contact.name}} fala de um chamado que já existe: {{tickets.first_subject}}.").
		From("related", "false").Ticket("ticket", "Suporte - {{contact.name}}", "medium").
		Handoff("h_tech", "queue.technical", "Suporte técnico. Cliente: {{contact.name}} {{customer.name}}. Empresa informada: {{empresa_informada}}.").
		From("menu", "fin").Handoff("h_fin", "queue.finance", "Financeiro. Cliente: {{contact.name}} {{customer.name}}.").
		From("menu", "com").Handoff("h_com", "queue.commercial", "Comercial. Contato: {{contact.name}} (empresa informada: {{empresa_informada}}).").
		From("menu", "outro").Handoff("h_fb", "queue.fallback", "Outro assunto. Contato: {{contact.name}}.").
		Connect("menu", "timeout", "h_fb").
		Var("assunto", "string", "Assunto escolhido no menu").Var("related", "string", "Definido pelo subflow existing-ticket").
		Var("nome", "string", "Definido pelo subflow unknown-contact").Var("empresa_informada", "string", "Definido pelo subflow unknown-contact")
	return mk("smart-reception", 1, "Recepção inteligente", "Recepção padrão: identifica o contato, resolve a empresa, evita chamado duplicado e encaminha pelo menu.",
		domain.FlowTypeInbound, generalCats, Settings{Priority: 999, IsDefault: true}, b, []TestCase{
			{Name: "cliente com uma empresa pede suporte e abre chamado", Scenario: Scenario{ContactKind: "customer", Companies: []string{"ACME"}, Events: Msg("oi", "1")},
				Expect: Expect{Status: "waiting_human", Say: []string{"Olá Contato Teste", "Como podemos ajudar?", "1) Suporte técnico"}, Effects: []string{"company_validated", "ticket", "handoff"}, Priority: "medium", Reaches: []string{"h_tech"}}},
			{Name: "cliente com chamado aberto não duplica", Scenario: Scenario{ContactKind: "customer", Companies: []string{"ACME"}, OpenTickets: 1, Events: Msg("oi", "1", "1")},
				Expect: Expect{Status: "waiting_human", Reaches: []string{"h_exist"}, NotReaches: []string{"ticket"}, Effects: []string{"company_validated", "handoff"}}},
			{Name: "contato desconhecido é apresentado e vai ao comercial", Scenario: Scenario{ContactKind: "unclassified", Events: Msg("oi", "João", "Acme", "3")},
				Expect: Expect{Status: "waiting_human", Reaches: []string{"unk", "h_com"}, NotReaches: []string{"ctx"}, Vars: map[string]string{"nome": "João"}, Effects: []string{"handoff"}}},
			{Name: "várias empresas: pergunta a empresa e depois o assunto", Scenario: Scenario{ContactKind: "customer", Companies: []string{"ACME", "Beta"}, Events: Msg("oi", "2", "2")},
				Expect: Expect{Status: "waiting_human", Reaches: []string{"h_fin"}, Effects: []string{"company_validated", "handoff"}}},
			{Name: "resposta fora do menu cai no assunto livre", Scenario: Scenario{ContactKind: "customer", Companies: []string{"ACME"}, Events: Msg("oi", "4")},
				Expect: Expect{Status: "waiting_human", Reaches: []string{"h_fb"}}},
			{Name: "sem resposta no menu vai para a triagem humana", Scenario: Scenario{ContactKind: "customer", Companies: []string{"ACME"}, Events: []Event{{Text: "oi"}, {Timeout: true}}},
				Expect: Expect{Status: "waiting_human", Reaches: []string{"h_fb"}}},
			{Name: "janela da Meta fechada não envia texto livre", Scenario: Scenario{Provider: "meta_cloud", WindowOpen: boolp(false), ContactKind: "customer", Events: Msg("oi")},
				Expect: Expect{Status: "failed", NoMessages: true}},
		}, recommended("general", "isp", "msp"))
}

var _ = domain.FlowTypeInbound
