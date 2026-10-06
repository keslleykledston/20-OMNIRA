package templates

import (
	"time"

	"github.com/omnira/omnira/internal/flows/domain"
)

var itCats = []string{"K3G", "TECHNICAL_SUPPORT", "NETWORK"}

// k3gHours is the starting schedule of the K3G pack (the tenant edits it after installing).
var k3gHours = domain.BusinessHoursConfig{Timezone: "America/Sao_Paulo", Windows: []domain.HoursWindow{{Days: []string{"mon", "tue", "wed", "thu", "fri"}, Start: "08:00", End: "18:00"}}}

func monday10() *time.Time { t := time.Date(2026, 10, 5, 13, 0, 0, 0, time.UTC); return &t } // 10:00 in São Paulo
func sunday10() *time.Time { t := time.Date(2026, 10, 4, 13, 0, 0, 0, time.UTC); return &t }

func k3gTemplates() []*Template {
	return []*Template{tK3GGeneralTriage(), tK3GVPN(), tK3GFirewall(), tK3GChange(), tK3GReception()}
}

func tK3GGeneralTriage() *Template {
	b := NewBuilder().Start().
		Steps("sev", "h_partial",
			AskS{ID: "desc", Text: "Descreva o problema com o máximo de detalhes (o que acontece, onde e desde quando).", Var: "descricao"},
			ChoiceS{ID: "impact", Text: "Qual é o impacto no seu negócio?", Var: "impacto", Opts: []Opt{{"parado", "Serviço parado", "parado"}, {"degradado", "Serviço degradado", "degradado"}, {"duvida", "Dúvida / sem impacto", "duvida"}}}).
		Switch("sev", "impacto", Case{ID: "parado", Value: "parado"}, Case{ID: "degradado", Value: "degradado"}).
		From("sev", "parado").Ticket("t_high", "{{area}} - {{contact.name}} ({{customer.name}})", "high").
		Handoff("h", "queue.technical", "{{area}}. Impacto: {{impacto}}. Descrição: {{descricao}}. Contato: {{contact.name}} {{customer.name}}.").
		From("sev", "degradado").Ticket("t_med", "{{area}} - {{contact.name}} ({{customer.name}})", "medium").Connect("t_med", "next", "h").
		From("sev", "default").Ticket("t_low", "{{area}} - {{contact.name}} ({{customer.name}})", "low").Connect("t_low", "next", "h").
		Handoff("h_partial", "queue.technical", "Triagem incompleta ({{area}}). Descrição até agora: {{descricao}}.").
		Connect("t_high", "error", "h_partial").Connect("t_med", "error", "h_partial").Connect("t_low", "error", "h_partial").
		Var("area", "string", "Área do atendimento, definida pelo fluxo que chama").Var("descricao", "string", "Descrição do problema").Var("impacto", "string", "parado|degradado|duvida")
	return mk("k3g-general-triage", 1, "K3G - Triagem técnica geral", "Coleta descrição e impacto e abre o chamado com prioridade determinística pelo impacto.",
		domain.FlowTypeSubflow, itCats, Settings{}, b, []TestCase{
			{Name: "serviço parado é prioridade alta", Scenario: Scenario{Events: Msg("oi", "tudo fora", "1")},
				Expect: Expect{Status: "waiting_human", Priority: "high", Effects: []string{"ticket", "handoff"}, Vars: map[string]string{"impacto": "parado"}}},
			{Name: "degradado é média", Scenario: Scenario{Events: Msg("oi", "lento", "2")}, Expect: Expect{Status: "waiting_human", Priority: "medium"}},
			{Name: "dúvida é baixa", Scenario: Scenario{Events: Msg("oi", "como faço?", "3")}, Expect: Expect{Status: "waiting_human", Priority: "low"}},
			{Name: "abandono entrega o que foi coletado", Scenario: Scenario{Events: []Event{{Text: "oi"}, {Text: "problema na rede"}, {Timeout: true}}},
				Expect: Expect{Status: "waiting_human", Reaches: []string{"h_partial"}, NotReaches: []string{"ticket"}}},
		}, recommended("msp", "isp"), withFeatures([]string{"conversations", "ticketing"}, nil))
}

func tK3GVPN() *Template {
	b := NewBuilder().Start().
		Steps("rule", "h_partial",
			ChoiceS{ID: "tipo", Text: "A VPN é site-to-site ou de usuário?", Var: "vpn_tipo", Opts: []Opt{{"s2s", "Site-to-site", "site-to-site"}, {"usr", "VPN de usuário", "usuario"}}},
			ChoiceS{ID: "scope", Text: "Quantos usuários/sites estão afetados?", Var: "vpn_escopo", Opts: []Opt{{"todos", "Todos", "todos"}, {"alguns", "Alguns", "alguns"}, {"um", "Apenas um", "um"}}},
			AskS{ID: "erro", Text: "Qual mensagem de erro aparece? (ou descreva o que acontece)", Var: "vpn_erro"},
			ChoiceS{ID: "antes", Text: "A VPN já funcionou antes?", Var: "vpn_antes", Opts: YesNo("Sim", "Não")},
			AskS{ID: "inicio", Text: "Quando começou o problema?", Var: "vpn_inicio"},
			ChoiceS{ID: "internet", Text: "A internet local está funcionando?", Var: "vpn_internet", Opts: YesNo("Sim", "Não")}).
		Condition("rule", "vpn_escopo", "eq", "todos").
		From("rule", "true").Ticket("t_high", "VPN {{vpn_tipo}} - {{contact.name}} ({{customer.name}})", "high").
		Handoff("h", "queue.technical", "VPN {{vpn_tipo}}; escopo: {{vpn_escopo}}; erro: {{vpn_erro}}; funcionou antes: {{vpn_antes}}; início: {{vpn_inicio}}; internet local: {{vpn_internet}}.").
		From("rule", "false").Ticket("t_med", "VPN {{vpn_tipo}} - {{contact.name}} ({{customer.name}})", "medium").Connect("t_med", "next", "h").
		Handoff("h_partial", "queue.technical", "Triagem de VPN incompleta. Tipo: {{vpn_tipo}}; escopo: {{vpn_escopo}}; erro: {{vpn_erro}}.").
		Connect("t_high", "error", "h_partial").Connect("t_med", "error", "h_partial")
	return mk("k3g-vpn-support", 1, "K3G - Suporte de VPN", "Triagem de VPN (tipo, escopo, erro, histórico, internet local) com prioridade alta quando afeta todos.",
		domain.FlowTypeSubflow, itCats, Settings{}, b, []TestCase{
			{Name: "afeta todos: prioridade alta", Scenario: Scenario{Events: Msg("oi", "1", "1", "AUTH_FAILED", "1", "ontem", "1")},
				Expect: Expect{Status: "waiting_human", Priority: "high", Effects: []string{"ticket", "handoff"}, Vars: map[string]string{"vpn_tipo": "site-to-site", "vpn_escopo": "todos", "vpn_erro": "AUTH_FAILED"}}},
			{Name: "afeta um usuário: prioridade média", Scenario: Scenario{Events: Msg("oi", "2", "3", "não conecta", "2", "hoje", "1")},
				Expect: Expect{Status: "waiting_human", Priority: "medium", Vars: map[string]string{"vpn_tipo": "usuario", "vpn_escopo": "um"}}},
			{Name: "abandono: humano recebe o parcial", Scenario: Scenario{Events: []Event{{Text: "oi"}, {Text: "1"}, {Timeout: true}}},
				Expect: Expect{Status: "waiting_human", Reaches: []string{"h_partial"}, NotReaches: []string{"t_high", "t_med"}}},
		}, recommended("msp", "isp"), withFeatures([]string{"conversations", "ticketing"}, nil))
}

func tK3GFirewall() *Template {
	b := NewBuilder().Start().
		Steps("rule", "h_partial",
			AskS{ID: "src", Text: "Qual é a origem (IP, rede ou nome do equipamento)?", Var: "fw_origem"},
			AskS{ID: "dst", Text: "Qual é o destino (IP, rede ou nome)?", Var: "fw_destino"},
			AskS{ID: "porta", Text: "Qual porta/protocolo (ex.: TCP 443)?", Var: "fw_porta"},
			AskS{ID: "app", Text: "Qual aplicação ou serviço é afetado?", Var: "fw_aplicacao"},
			AskS{ID: "quando", Text: "Em que dia/horário o problema ocorre?", Var: "fw_horario"},
			ChoiceS{ID: "antes", Text: "Esse acesso já funcionou antes?", Var: "fw_antes", Opts: YesNo("Sim", "Não")},
			ChoiceS{ID: "escopo", Text: "Quantos usuários são afetados?", Var: "fw_escopo", Opts: []Opt{{"todos", "Todos", "todos"}, {"alguns", "Alguns", "alguns"}, {"um", "Apenas um", "um"}}}).
		Condition("rule", "fw_escopo", "eq", "todos").
		From("rule", "true").Ticket("t_high", "Firewall - {{fw_origem}} -> {{fw_destino}} ({{fw_porta}})", "high").
		Say("note", "Registramos a solicitação. Nenhuma alteração é feita automaticamente: um analista vai avaliar o caso.").
		Handoff("h", "queue.technical", "Firewall. Origem: {{fw_origem}}; destino: {{fw_destino}}; porta: {{fw_porta}}; aplicação: {{fw_aplicacao}}; horário: {{fw_horario}}; funcionava: {{fw_antes}}; escopo: {{fw_escopo}}. NENHUMA mudança foi executada pelo bot.").
		From("rule", "false").Ticket("t_med", "Firewall - {{fw_origem}} -> {{fw_destino}} ({{fw_porta}})", "medium").Connect("t_med", "next", "note").
		Handoff("h_partial", "queue.technical", "Triagem de firewall incompleta. Origem: {{fw_origem}}; destino: {{fw_destino}}; porta: {{fw_porta}}.").
		Connect("t_high", "error", "h_partial").Connect("t_med", "error", "h_partial").Connect("note", "error", "h").Connect("note", "window_closed", "h")
	return mk("k3g-firewall-support", 1, "K3G - Suporte de firewall", "Coleta origem, destino, porta, aplicação, horário e escopo. Nunca executa mudança: só abre chamado.",
		domain.FlowTypeSubflow, itCats, Settings{}, b, []TestCase{
			{Name: "todos afetados: alta, sem mudança automática", Scenario: Scenario{Events: Msg("oi", "10.0.0.5", "200.1.1.1", "TCP 443", "ERP", "hoje 9h", "1", "1")},
				Expect: Expect{Status: "waiting_human", Priority: "high", Say: []string{"Nenhuma alteração é feita automaticamente"}, Effects: []string{"ticket", "handoff"}}},
			{Name: "um usuário: média", Scenario: Scenario{Events: Msg("oi", "10.0.0.5", "200.1.1.1", "UDP 53", "DNS", "agora", "2", "3")}, Expect: Expect{Status: "waiting_human", Priority: "medium"}},
			{Name: "abandono", Scenario: Scenario{Events: []Event{{Text: "oi"}, {Text: "10.0.0.5"}, {Timeout: true}}}, Expect: Expect{Status: "waiting_human", Reaches: []string{"h_partial"}}},
		}, recommended("msp", "isp"), withFeatures([]string{"conversations", "ticketing"}, nil))
}

func tK3GChange() *Template {
	b := NewBuilder().Start().
		Steps("env_rule", "h_partial",
			ChoiceS{ID: "tipo", Text: "Que tipo de mudança você precisa?", Var: "mud_tipo", Opts: []Opt{{"firewall", "Firewall", "firewall"}, {"nat", "NAT", "nat"}, {"rota", "Rota", "rota"}, {"dns", "DNS", "dns"}, {"vlan", "VLAN", "vlan"}, {"vpn", "VPN", "vpn"}, {"config", "Configuração", "configuracao"}}},
			AskS{ID: "equip", Text: "Em qual equipamento ou sistema?", Var: "mud_equip"},
			ChoiceS{ID: "amb", Text: "O ambiente é de produção ou homologação?", Var: "mud_ambiente", Opts: []Opt{{"prod", "Produção", "producao"}, {"hml", "Homologação", "homologacao"}}},
			AskS{ID: "desc", Text: "Descreva a alteração solicitada.", Var: "mud_descricao"},
			AskS{ID: "janela", Text: "Qual a janela de mudança desejada (data e horário)?", Var: "mud_janela"},
			AskS{ID: "impacto", Text: "Qual o impacto esperado durante a mudança?", Var: "mud_impacto"},
			AskS{ID: "autor", Text: "Quem autorizou esta mudança (nome e cargo)?", Var: "mud_autorizacao"}).
		Condition("env_rule", "mud_ambiente", "eq", "producao").
		From("env_rule", "true").Ticket("t_high", "Mudança {{mud_tipo}} - {{mud_equip}} [{{mud_ambiente}}]", "high").
		Say("note", "Solicitação de mudança registrada. Ela será avaliada antes de qualquer execução.").
		Handoff("h", "queue.technical", "MUDANÇA {{mud_tipo}} em {{mud_equip}} ({{mud_ambiente}}). Descrição: {{mud_descricao}}. Janela: {{mud_janela}}. Impacto: {{mud_impacto}}. Solicitante: {{contact.name}} {{customer.name}}. Autorização: {{mud_autorizacao}}.").
		From("env_rule", "false").Ticket("t_med", "Mudança {{mud_tipo}} - {{mud_equip}} [{{mud_ambiente}}]", "medium").Connect("t_med", "next", "note").
		Handoff("h_partial", "queue.technical", "Solicitação de mudança incompleta. Tipo: {{mud_tipo}}; equipamento: {{mud_equip}}.").
		Connect("t_high", "error", "h_partial").Connect("t_med", "error", "h_partial").Connect("note", "error", "h").Connect("note", "window_closed", "h")
	return mk("k3g-change-request", 1, "K3G - Solicitação de mudança", "Captura tipo, equipamento, ambiente, janela, impacto, solicitante e autorização, e registra a solicitação sem executar nada.",
		domain.FlowTypeSubflow, itCats, Settings{}, b, []TestCase{
			{Name: "mudança em produção é alta e registra autorização", Scenario: Scenario{Events: Msg("oi", "1", "FW-01", "1", "liberar 8443", "sábado 22h", "queda de 1 min", "Maria, gerente de TI")},
				Expect: Expect{Status: "waiting_human", Priority: "high", Say: []string{"Ela será avaliada antes de qualquer execução"}, Vars: map[string]string{"mud_tipo": "firewall", "mud_autorizacao": "Maria, gerente de TI"}}},
			{Name: "homologação é média", Scenario: Scenario{Events: Msg("oi", "6", "VPN-HQ", "2", "novo peer", "amanhã", "nenhum", "João")}, Expect: Expect{Status: "waiting_human", Priority: "medium"}},
			{Name: "abandono", Scenario: Scenario{Events: []Event{{Text: "oi"}, {Text: "2"}, {Timeout: true}}}, Expect: Expect{Status: "waiting_human", Reaches: []string{"h_partial"}, NotReaches: []string{"t_high", "t_med"}}},
		}, recommended("msp"), withFeatures([]string{"conversations", "ticketing"}, nil))
}

func tK3GReception() *Template {
	b := NewBuilder().Start().
		Say("hello", "Olá {{contact.name}}! Você está no atendimento da K3G Solutions.").
		Hours("hours", k3gHours).
		From("hours", "closed").Sub("closed", "after-hours").End("end_closed", "informational").
		From("hours", "open").Contact("who").
		From("who", "known").Sub("ctx", "customer-context").
		Choice("menu", "Como podemos ajudar?", "assunto",
			Opt{"tec", "Suporte técnico", ""}, Opt{"con", "Conectividade", ""}, Opt{"mud", "Mudança", ""}, Opt{"fin", "Financeiro", ""},
			Opt{"com", "Comercial", ""}, Opt{"prj", "Projeto / implantação", ""}, Opt{"out", "Outro", ""}).
		From("who", "unknown").Sub("unk", "unknown-contact").Connect("unk", "next", "menu").
		From("menu", "tec").Choice("tmenu", "Qual é o tipo de problema?", "area_tec", Opt{"vpn", "VPN", ""}, Opt{"fw", "Firewall", ""}, Opt{"gen", "Outro problema técnico", ""}).
		From("tmenu", "vpn").Sub("s_vpn", "k3g-vpn-support").End("end_after", "informational").
		From("tmenu", "fw").Sub("s_fw", "k3g-firewall-support").Connect("s_fw", "next", "end_after").
		From("tmenu", "gen").Set("area_tec", "area", "Suporte técnico").Sub("s_gen", "k3g-general-triage").Connect("s_gen", "next", "end_after").
		From("menu", "con").Set("area_con", "area", "Conectividade").Sub("s_con", "k3g-general-triage").Connect("s_con", "next", "end_after").
		From("menu", "mud").Sub("s_mud", "k3g-change-request").Connect("s_mud", "next", "end_after").
		From("menu", "fin").Handoff("h_fin", "queue.finance", "Financeiro. Contato: {{contact.name}} {{customer.name}}.").
		From("menu", "com").Handoff("h_com", "queue.commercial", "Comercial. Contato: {{contact.name}} (empresa informada: {{empresa_informada}}).").
		From("menu", "prj").Handoff("h_prj", "queue.projects", "Projeto/implantação. Contato: {{contact.name}} {{customer.name}}.").
		From("menu", "out").Handoff("h_fb", "queue.fallback", "Outro assunto. Contato: {{contact.name}}.").
		Connect("menu", "timeout", "h_fb").Connect("tmenu", "timeout", "h_fb").
		Var("area", "string", "Área do atendimento (usada pelos subflows)").Var("assunto", "string", "").Var("area_tec", "string", "").
		Var("nome", "string", "Definido por unknown-contact").Var("empresa_informada", "string", "Definido por unknown-contact")
	return mk("k3g-central-reception", 1, "K3G - Recepção central", "Recepção da K3G: horário comercial, identificação, empresa e menu (técnico, conectividade, mudança, financeiro, comercial, projeto).",
		domain.FlowTypeInbound, itCats, Settings{Priority: 100, IsDefault: true}, b, []TestCase{
			{Name: "fora do horário usa o fluxo de fora do horário", Scenario: Scenario{Now: sunday10(), Events: Msg("oi")},
				Expect: Expect{Status: "completed", Reaches: []string{"closed"}, NotReaches: []string{"who", "menu"}, Say: []string{"Olá", "fora do horário"}, Effects: []string{"ticket"}}},
			{Name: "cliente pede VPN: triagem de VPN com prioridade alta", Scenario: Scenario{Now: monday10(), ContactKind: "customer", Companies: []string{"ACME"}, Events: Msg("oi", "1", "1", "1", "1", "AUTH_FAILED", "1", "ontem", "1")},
				Expect: Expect{Status: "waiting_human", Reaches: []string{"s_vpn", "t_high"}, Priority: "high", Effects: []string{"company_validated", "ticket", "handoff"}}},
			{Name: "conectividade usa a triagem geral com a área definida", Scenario: Scenario{Now: monday10(), ContactKind: "customer", Companies: []string{"ACME"}, Events: Msg("oi", "2", "link caiu", "1")},
				Expect: Expect{Status: "waiting_human", Reaches: []string{"s_con"}, Vars: map[string]string{"area": "Conectividade"}, Priority: "high"}},
			{Name: "mudança segue a solicitação de mudança", Scenario: Scenario{Now: monday10(), ContactKind: "customer", Companies: []string{"ACME"}, Events: Msg("oi", "3", "1", "FW-01", "1", "liberar porta", "sábado", "mínimo", "Maria")},
				Expect: Expect{Status: "waiting_human", Reaches: []string{"s_mud"}, Priority: "high"}},
			{Name: "financeiro vai direto para a fila", Scenario: Scenario{Now: monday10(), ContactKind: "customer", Companies: []string{"ACME"}, Events: Msg("oi", "4")},
				Expect: Expect{Status: "waiting_human", Reaches: []string{"h_fin"}, Effects: []string{"company_validated", "handoff"}}},
			{Name: "contato desconhecido é apresentado antes do menu", Scenario: Scenario{Now: monday10(), ContactKind: "unclassified", Events: Msg("oi", "Pedro", "Beta SA", "5")},
				Expect: Expect{Status: "waiting_human", Reaches: []string{"unk", "h_com"}, NotReaches: []string{"ctx"}, Vars: map[string]string{"nome": "Pedro"}}},
			{Name: "sem resposta no menu: triagem humana", Scenario: Scenario{Now: monday10(), ContactKind: "customer", Companies: []string{"ACME"}, Events: []Event{{Text: "oi"}, {Timeout: true}}},
				Expect: Expect{Status: "waiting_human", Reaches: []string{"h_fb"}}},
		}, recommended("msp", "isp"), intermediate(), withFeatures([]string{"conversations", "ticketing"}, nil))
}
