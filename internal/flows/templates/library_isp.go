package templates

import "github.com/omnira/omnira/internal/flows/domain"

var ispCats = []string{"ISP", "NOC", "NETWORK"}

func ispTemplates() []*Template {
	return []*Template{tLinkDown(), tPerformance(), tIntermittency(), tBGP(), tDNS(), tEquipmentOffline(), tONUOffline(), tCollective(), tCritical(), tFinancial(), tCommercial(), tISPReception()}
}

func ispTpl(slug, name, desc string, b *Builder, tests []TestCase) *Template {
	return mk(slug, 1, name, desc, domain.FlowTypeSubflow, ispCats, Settings{}, b, tests,
		recommended("isp"), intermediate(), withFeatures([]string{"conversations", "ticketing"}, []string{"monitoring"}))
}

// Severity is ALWAYS a deterministic rule over what the contact declared (and the ticket history); an AI may suggest, it never
// sets critical. The rules are in the flow, where the tenant can read and change them.

func tLinkDown() *Template {
	b := NewBuilder().Start().Set("cat1", "categoria", "CONNECTIVITY").Set("cat2", "subcategoria", "LINK_DOWN").
		Steps("kind", "h_partial",
			AskS{ID: "circuito", Text: "Qual é o circuito/link afetado? (identificação ou endereço)", Var: "circuito"},
			AskS{ID: "operadora", Text: "Qual é a operadora do link?", Var: "operadora"},
			AskS{ID: "inicio", Text: "Desde que horas o link está fora?", Var: "inicio"},
			ChoiceS{ID: "abr", Text: "A queda é total ou parcial?", Var: "abrangencia", Opts: []Opt{{"total", "Total (sem tráfego)", "total"}, {"parcial", "Parcial (com falhas)", "parcial"}}},
			ChoiceS{ID: "backup", Text: "Existe link de backup?", Var: "backup", Opts: []Opt{{"nao", "Não existe", "nao"}, {"operando", "Existe e está operando", "operando"}, {"fora", "Existe, mas também está fora", "fora"}}},
			ChoiceS{ID: "energia", Text: "O equipamento local está ligado (energia e LEDs acesos)?", Var: "energia", Opts: []Opt{{"sim", "Sim", "sim"}, {"nao", "Não", "nao"}, {"naosei", "Não sei", "naosei"}}}).
		Condition("kind", "abrangencia", "eq", "total").
		From("kind", "true").Switch("bk", "backup", Case{ID: "nao", Value: "nao"}, Case{ID: "fora", Value: "fora"}).
		From("bk", "nao").Ticket("t_crit", "Link down - {{circuito}} ({{customer.name}})", "critical").
		Handoff("h", "queue.noc", "{{categoria}}/{{subcategoria}}. Circuito: {{circuito}} ({{operadora}}); início: {{inicio}}; abrangência: {{abrangencia}}; backup: {{backup}}; equipamento local ligado: {{energia}}. Severidade pelas regras do fluxo. Cliente: {{customer.name}}.").
		Connect("bk", "fora", "t_crit").
		From("bk", "default").Ticket("t_high", "Link down - {{circuito}} ({{customer.name}})", "high").Connect("t_high", "next", "h").
		From("kind", "false").Ticket("t_med", "Link parcial - {{circuito}} ({{customer.name}})", "medium").Connect("t_med", "next", "h").
		Handoff("h_partial", "queue.noc", "Triagem de link incompleta. Circuito: {{circuito}}; início: {{inicio}}.").
		Connect("t_crit", "error", "h_partial").Connect("t_high", "error", "h_partial").Connect("t_med", "error", "h_partial").
		Var("categoria", "string", "").Var("subcategoria", "string", "")
	return ispTpl("isp-link-down", "ISP - Link/circuito fora", "Coleta circuito, operadora, horário, abrangência, backup e energia. Severidade crítica só por regra: queda total sem backup operando.", b, []TestCase{
		{Name: "queda total sem backup é crítica", Scenario: Scenario{Events: Msg("oi", "POA-123", "Algar", "14:30", "1", "1", "1")},
			Expect: Expect{Status: "waiting_human", Priority: "critical", Effects: []string{"ticket", "handoff"}, Vars: map[string]string{"categoria": "CONNECTIVITY", "subcategoria": "LINK_DOWN", "circuito": "POA-123"}}},
		{Name: "queda total com backup também fora é crítica", Scenario: Scenario{Events: Msg("oi", "POA-123", "Algar", "14:30", "1", "3", "1")}, Expect: Expect{Status: "waiting_human", Priority: "critical"}},
		{Name: "queda total com backup operando é alta", Scenario: Scenario{Events: Msg("oi", "POA-123", "Algar", "14:30", "1", "2", "1")}, Expect: Expect{Status: "waiting_human", Priority: "high"}},
		{Name: "queda parcial é média", Scenario: Scenario{Events: Msg("oi", "POA-123", "Algar", "14:30", "2", "1", "1")}, Expect: Expect{Status: "waiting_human", Priority: "medium"}},
		{Name: "abandono entrega o parcial e não abre chamado", Scenario: Scenario{Events: []Event{{Text: "oi"}, {Text: "POA-123"}, {Timeout: true}}}, Expect: Expect{Status: "waiting_human", Reaches: []string{"h_partial"}, NotReaches: []string{"t_crit", "t_high", "t_med"}}},
	})
}

func tPerformance() *Template {
	b := NewBuilder().Start().Set("cat", "categoria", "PERFORMANCE").
		Steps("rule", "h_partial",
			ChoiceS{ID: "sintoma", Text: "Qual é o sintoma principal?", Var: "sintoma", Opts: []Opt{{"lentidao", "Lentidão", "lentidao"}, {"latencia", "Latência alta", "latencia"}, {"perda", "Perda de pacotes", "perda de pacotes"}, {"vazao", "Baixa vazão", "baixa vazao"}, {"destino", "Só alguns destinos", "destino especifico"}}},
			AskS{ID: "inicio", Text: "Quando começou?", Var: "inicio"},
			ChoiceS{ID: "padrao", Text: "É contínuo ou intermitente?", Var: "padrao", Opts: []Opt{{"continuo", "Contínuo", "continuo"}, {"intermitente", "Intermitente", "intermitente"}}},
			ChoiceS{ID: "destinos", Text: "Afeta todos os destinos?", Var: "todos_destinos", Opts: YesNo("Sim, todos", "Não, só alguns")},
			AskS{ID: "afetados", Text: "Quantos usuários/locais estão afetados? (número)", Var: "afetados", Validation: "number"},
			ChoiceS{ID: "backup", Text: "Existe link de backup?", Var: "backup", Opts: YesNo("Sim", "Não")}).
		Condition("rule", "todos_destinos", "eq", "sim").
		From("rule", "true").Ticket("t_high", "Desempenho ({{sintoma}}) - {{customer.name}}", "high").
		Handoff("h", "queue.noc", "{{categoria}}. Sintoma: {{sintoma}} ({{padrao}}); início: {{inicio}}; todos os destinos: {{todos_destinos}}; afetados: {{afetados}}; backup: {{backup}}. Verificações sugeridas ao analista: utilização do circuito, latência e perda de pacotes (integração de monitoramento opcional, não configurada).").
		From("rule", "false").Ticket("t_med", "Desempenho ({{sintoma}}) - {{customer.name}}", "medium").Connect("t_med", "next", "h").
		Handoff("h_partial", "queue.noc", "Triagem de desempenho incompleta. Sintoma: {{sintoma}}; início: {{inicio}}.").
		Connect("t_high", "error", "h_partial").Connect("t_med", "error", "h_partial").
		Var("categoria", "string", "")
	return ispTpl("isp-performance", "ISP - Lentidão, latência e perda", "Classifica o sintoma de desempenho e abre o chamado; alta quando afeta todos os destinos. Funciona sem integração de monitoramento.", b, []TestCase{
		{Name: "afeta todos os destinos: alta", Scenario: Scenario{Events: Msg("oi", "3", "hoje cedo", "1", "1", "40", "1")}, Expect: Expect{Status: "waiting_human", Priority: "high", Vars: map[string]string{"sintoma": "perda de pacotes", "afetados": "40"}}},
		{Name: "só alguns destinos: média", Scenario: Scenario{Events: Msg("oi", "5", "ontem", "2", "2", "3", "2")}, Expect: Expect{Status: "waiting_human", Priority: "medium"}},
		{Name: "afetados precisa ser número", Scenario: Scenario{Events: Msg("oi", "1", "hoje", "1", "1", "muitos")}, Expect: Expect{Status: "waiting_input", WaitingAt: "afetados"}},
	})
}

func tIntermittency() *Template {
	b := NewBuilder().Start().Set("cat", "categoria", "INTERMITTENCY").
		Steps("rec", "h_partial",
			AskS{ID: "freq", Text: "Com que frequência a conexão cai?", Var: "frequencia"},
			AskS{ID: "dur", Text: "Quanto tempo dura cada queda?", Var: "duracao"},
			AskS{ID: "primeira", Text: "Quando foi a primeira ocorrência?", Var: "primeira"},
			AskS{ID: "ultima", Text: "Quando foi a última?", Var: "ultima"},
			AskS{ID: "afetados", Text: "Quais usuários ou locais são afetados?", Var: "afetados"},
			AskS{ID: "circuito", Text: "Qual é o circuito?", Var: "circuito"},
			AskS{ID: "equip", Text: "Qual equipamento está envolvido?", Var: "equipamento"}).
		FindTickets("rec").
		From("rec", "found").Ticket("t_high", "Intermitência RECORRENTE - {{circuito}} ({{customer.name}})", "high").
		Handoff("h", "queue.noc", "{{categoria}}. Frequência: {{frequencia}}; duração: {{duracao}}; primeira: {{primeira}}; última: {{ultima}}; afetados: {{afetados}}; circuito: {{circuito}}; equipamento: {{equipamento}}. Histórico: o cliente já tem chamado aberto ({{tickets.first_subject}}), possível problema recorrente.").
		From("rec", "none").Ticket("t_med", "Intermitência - {{circuito}} ({{customer.name}})", "medium").Connect("t_med", "next", "h").
		Handoff("h_partial", "queue.noc", "Triagem de intermitência incompleta. Circuito: {{circuito}}.").
		Connect("t_high", "error", "h_partial").Connect("t_med", "error", "h_partial").
		Var("categoria", "string", "")
	return ispTpl("isp-intermittency", "ISP - Conexão intermitente", "Frequência, duração, ocorrências, afetados, circuito e equipamento; histórico de chamados abertos torna o caso recorrente (alta).", b, []TestCase{
		{Name: "sem histórico: média", Scenario: Scenario{Events: Msg("oi", "3x ao dia", "2 min", "segunda", "hoje", "matriz", "POA-1", "roteador")}, Expect: Expect{Status: "waiting_human", Priority: "medium", Vars: map[string]string{"circuito": "POA-1"}}},
		{Name: "já tem chamado aberto: recorrente, alta", Scenario: Scenario{OpenTickets: 1, Events: Msg("oi", "3x ao dia", "2 min", "segunda", "hoje", "matriz", "POA-1", "roteador")}, Expect: Expect{Status: "waiting_human", Priority: "high", Reaches: []string{"t_high"}}},
	})
}

func tBGP() *Template {
	b := NewBuilder().Start().Set("cat", "categoria", "ROUTING").
		Steps("sev", "h_partial",
			ChoiceS{ID: "tipo", Text: "Qual é o problema?", Var: "bgp_problema", Opts: []Opt{{"sessao", "Sessão BGP caiu", "sessao"}, {"nao_anunciado", "Prefixo não anunciado", "prefixo nao anunciado"}, {"nao_recebido", "Prefixo não recebido", "prefixo nao recebido"}, {"roteamento", "Problema de roteamento", "roteamento"}, {"assimetria", "Assimetria", "assimetria"}, {"transito", "Trânsito", "transito"}, {"peering", "Peering", "peering"}, {"outro", "Outro", "outro"}}},
			AskS{ID: "asn_local", Text: "Qual é o seu ASN (local)? (número)", Var: "asn_local", Validation: "number"},
			AskS{ID: "asn_remoto", Text: "Qual é o ASN remoto? (número)", Var: "asn_remoto", Validation: "number"},
			AskS{ID: "peer", Text: "Qual é o IP do peer?", Var: "peer_ip"},
			AskS{ID: "prefixo", Text: "Qual prefixo está afetado?", Var: "prefixo"},
			ChoiceS{ID: "ipver", Text: "IPv4, IPv6 ou ambos?", Var: "ip_versao", Opts: []Opt{{"v4", "IPv4", "ipv4"}, {"v6", "IPv6", "ipv6"}, {"ambos", "Ambos", "ambos"}}},
			AskS{ID: "inicio", Text: "Quando começou?", Var: "inicio"},
			ChoiceS{ID: "impacto", Text: "Qual é o impacto?", Var: "impacto", Opts: []Opt{{"total", "Total", "total"}, {"parcial", "Parcial", "parcial"}, {"nenhum", "Sem impacto visível", "nenhum"}}}).
		Switch("sev", "impacto", Case{ID: "total", Value: "total"}, Case{ID: "parcial", Value: "parcial"}).
		From("sev", "total").Condition("crit", "bgp_problema", "eq", "sessao").
		From("crit", "true").Ticket("t_crit", "BGP/Roteamento - AS{{asn_remoto}} {{prefixo}}", "critical").
		Handoff("h", "queue.noc_l2l3", "{{categoria}}: {{bgp_problema}}. AS local {{asn_local}} / remoto {{asn_remoto}}; peer {{peer_ip}}; prefixo {{prefixo}} ({{ip_versao}}); início: {{inicio}}; impacto: {{impacto}}.").
		From("crit", "false").Ticket("t_high", "BGP/Roteamento - AS{{asn_remoto}} {{prefixo}}", "high").Connect("t_high", "next", "h").
		From("sev", "parcial").Ticket("t_med", "BGP/Roteamento - AS{{asn_remoto}} {{prefixo}}", "medium").Connect("t_med", "next", "h").
		From("sev", "default").Ticket("t_low", "BGP/Roteamento - AS{{asn_remoto}} {{prefixo}}", "low").Connect("t_low", "next", "h").
		Handoff("h_partial", "queue.noc_l2l3", "Triagem de BGP/roteamento incompleta. Problema: {{bgp_problema}}; peer: {{peer_ip}}.").
		Connect("t_crit", "error", "h_partial").Connect("t_high", "error", "h_partial").Connect("t_med", "error", "h_partial").Connect("t_low", "error", "h_partial").
		Var("categoria", "string", "")
	return ispTpl("isp-bgp-routing", "ISP - BGP e roteamento", "Sessão BGP, prefixos, roteamento, assimetria, trânsito e peering para o NOC L2/L3. Crítico só com sessão caída e impacto total.", b, []TestCase{
		{Name: "sessão caída com impacto total é crítica", Scenario: Scenario{Events: Msg("oi", "1", "65001", "65002", "10.0.0.1", "200.1.0.0/24", "1", "hoje", "1")}, Expect: Expect{Status: "waiting_human", Priority: "critical", Reaches: []string{"t_crit"}, Vars: map[string]string{"asn_local": "65001", "bgp_problema": "sessao"}}},
		{Name: "outro problema com impacto total é alta", Scenario: Scenario{Events: Msg("oi", "4", "65001", "65002", "10.0.0.1", "200.1.0.0/24", "1", "hoje", "1")}, Expect: Expect{Status: "waiting_human", Priority: "high"}},
		{Name: "impacto parcial é média", Scenario: Scenario{Events: Msg("oi", "7", "65001", "65002", "10.0.0.1", "200.1.0.0/24", "2", "hoje", "2")}, Expect: Expect{Status: "waiting_human", Priority: "medium"}},
		{Name: "sem impacto é baixa", Scenario: Scenario{Events: Msg("oi", "6", "65001", "65002", "10.0.0.1", "200.1.0.0/24", "3", "hoje", "3")}, Expect: Expect{Status: "waiting_human", Priority: "low"}},
		{Name: "ASN precisa ser número", Scenario: Scenario{Events: Msg("oi", "1", "AS65001")}, Expect: Expect{Status: "waiting_input", WaitingAt: "asn_local"}},
	})
}

func tDNS() *Template {
	b := NewBuilder().Start().Set("cat1", "categoria", "NETWORK_SERVICES").Set("cat2", "subcategoria", "DNS").
		Choice("escopo", "A falha é em todos os domínios ou em um específico?", "dns_escopo", Opt{"todos", "Todos os domínios", "todos"}, Opt{"especifico", "Um domínio específico", "especifico"}).
		From("escopo", "especifico").Ask("dominio", "Qual é o domínio?", "dominio").
		Steps("rule", "h_partial",
			ChoiceS{ID: "resolv", Text: "Qual resolvedor está em uso?", Var: "resolvedor", Opts: []Opt{{"cliente", "DNS do cliente", "cliente"}, {"publico", "Resolvedor público", "publico"}, {"nosso", "Nosso resolvedor", "nosso"}}},
			ChoiceS{ID: "ipver", Text: "Afeta IPv4, IPv6 ou ambos?", Var: "ip_versao", Opts: []Opt{{"v4", "IPv4", "ipv4"}, {"v6", "IPv6", "ipv6"}, {"ambos", "Ambos", "ambos"}}}).
		Connect("escopo", "todos", "resolv").Connect("escopo", "timeout", "h_partial").Connect("dominio", "timeout", "h_partial").
		Condition("rule", "dns_escopo", "eq", "todos").
		From("rule", "true").Ticket("t_high", "DNS (todos os domínios) - {{customer.name}}", "high").
		Handoff("h", "queue.noc", "{{categoria}}/{{subcategoria}}. Escopo: {{dns_escopo}} {{dominio}}; resolvedor: {{resolvedor}}; IP: {{ip_versao}}.").
		From("rule", "false").Ticket("t_med", "DNS ({{dominio}}) - {{customer.name}}", "medium").Connect("t_med", "next", "h").
		Handoff("h_partial", "queue.noc", "Triagem de DNS incompleta. Escopo: {{dns_escopo}}; domínio: {{dominio}}.").
		Connect("t_high", "error", "h_partial").Connect("t_med", "error", "h_partial").
		Var("categoria", "string", "").Var("subcategoria", "string", "").Var("dominio", "string", "Domínio informado (só no escopo específico)")
	return ispTpl("isp-dns", "ISP - DNS", "Falha de DNS: escopo, domínio, resolvedor e versão de IP. Alta quando afeta todos os domínios.", b, []TestCase{
		{Name: "todos os domínios: alta, sem perguntar o domínio", Scenario: Scenario{Events: Msg("oi", "1", "2", "3")}, Expect: Expect{Status: "waiting_human", Priority: "high", NotReaches: []string{"dominio"}, Vars: map[string]string{"subcategoria": "DNS"}}},
		{Name: "domínio específico: pergunta e é média", Scenario: Scenario{Events: Msg("oi", "2", "exemplo.com.br", "1", "1")}, Expect: Expect{Status: "waiting_human", Priority: "medium", Reaches: []string{"dominio"}, Vars: map[string]string{"dominio": "exemplo.com.br"}}},
	})
}

func tEquipmentOffline() *Template {
	b := NewBuilder().Start().Set("cat", "categoria", "EQUIPMENT").
		Steps("rule", "h_partial",
			ChoiceS{ID: "tipo", Text: "Qual equipamento está offline?", Var: "equipamento", Opts: []Opt{{"router", "Roteador", "roteador"}, {"switch", "Switch", "switch"}, {"fw", "Firewall", "firewall"}, {"olt", "OLT", "olt"}, {"onu", "ONU/ONT", "onu"}, {"ap", "Access point", "access point"}, {"radio", "Rádio", "radio"}, {"srv", "Servidor", "servidor"}, {"outro", "Outro", "outro"}}},
			ChoiceS{ID: "energia", Text: "O equipamento tem energia?", Var: "energia", Opts: []Opt{{"sim", "Sim", "sim"}, {"nao", "Não", "nao"}, {"naosei", "Não sei", "naosei"}}},
			AskS{ID: "leds", Text: "Quais LEDs estão acesos ou piscando?", Var: "leds"},
			ChoiceS{ID: "nobreak", Text: "Há nobreak?", Var: "nobreak", Opts: YesNo("Sim", "Não")},
			ChoiceS{ID: "outros", Text: "Os outros equipamentos do local respondem?", Var: "outros_respondem", Opts: YesNo("Sim", "Não, nenhum")},
			ChoiceS{ID: "queda", Text: "Houve queda de energia?", Var: "queda_energia", Opts: []Opt{{"sim", "Sim", "sim"}, {"nao", "Não", "nao"}, {"naosei", "Não sei", "naosei"}}},
			ChoiceS{ID: "manut", Text: "Havia manutenção programada?", Var: "manutencao", Opts: []Opt{{"sim", "Sim", "sim"}, {"nao", "Não", "nao"}, {"naosei", "Não sei", "naosei"}}}).
		Condition("rule", "outros_respondem", "eq", "nao").
		From("rule", "true").Ticket("t_high", "Equipamento offline ({{equipamento}}) - local inteiro fora - {{customer.name}}", "high").
		Handoff("h", "queue.noc", "{{categoria}}: {{equipamento}} offline. Energia: {{energia}}; LEDs: {{leds}}; nobreak: {{nobreak}}; outros equipamentos respondem: {{outros_respondem}}; queda de energia: {{queda_energia}}; manutenção: {{manutencao}}.").
		From("rule", "false").Ticket("t_med", "Equipamento offline ({{equipamento}}) - {{customer.name}}", "medium").Connect("t_med", "next", "h").
		Handoff("h_partial", "queue.noc", "Triagem de equipamento incompleta. Equipamento: {{equipamento}}; energia: {{energia}}.").
		Connect("t_high", "error", "h_partial").Connect("t_med", "error", "h_partial").
		Var("categoria", "string", "")
	return ispTpl("isp-equipment-offline", "ISP - Equipamento offline", "Roteador, switch, firewall, OLT, ONU, AP, rádio ou servidor: energia, LEDs, nobreak e o que mais responde no local.", b, []TestCase{
		{Name: "nada responde no local: alta", Scenario: Scenario{Events: Msg("oi", "1", "2", "nenhum", "2", "2", "1", "2")}, Expect: Expect{Status: "waiting_human", Priority: "high", Vars: map[string]string{"equipamento": "roteador", "energia": "nao"}}},
		{Name: "só o equipamento: média", Scenario: Scenario{Events: Msg("oi", "2", "1", "vermelho", "1", "1", "2", "2")}, Expect: Expect{Status: "waiting_human", Priority: "medium"}},
	})
}

func tONUOffline() *Template {
	b := NewBuilder().Start().Set("cat", "categoria", "FTTH").
		Steps("hip", "h_partial",
			AskS{ID: "contrato", Text: "Qual é o contrato ou nome do cliente?", Var: "contrato"},
			AskS{ID: "pop", Text: "Qual é o POP?", Var: "pop"},
			AskS{ID: "olt", Text: "Qual é a OLT?", Var: "olt"},
			AskS{ID: "pon", Text: "Qual é a PON?", Var: "pon"},
			AskS{ID: "serial", Text: "Qual é o serial da ONU?", Var: "onu_serial"},
			AskS{ID: "ultima", Text: "Quando foi a última vez online?", Var: "ultima_vez_online"},
			ChoiceS{ID: "los", Text: "Como está o LED LOS da ONU?", Var: "led_los", Opts: []Opt{{"apagado", "Apagado", "apagado"}, {"aceso", "Aceso", "aceso"}, {"piscando", "Piscando", "piscando"}}},
			ChoiceS{ID: "ledpon", Text: "Como está o LED PON?", Var: "led_pon", Opts: []Opt{{"apagado", "Apagado", "apagado"}, {"aceso", "Aceso", "aceso"}, {"piscando", "Piscando", "piscando"}}},
			ChoiceS{ID: "energia", Text: "A ONU tem energia?", Var: "energia", Opts: []Opt{{"sim", "Sim", "sim"}, {"nao", "Não", "nao"}}}).
		Condition("hip", "energia", "eq", "nao").
		From("hip", "true").Set("h_energia", "hipotese", "possível problema de energia no local (hipótese, sem confirmação)").Ticket("tk", "ONU offline - {{contrato}} ({{onu_serial}})", "medium").
		Handoff("h", "queue.noc", "{{categoria}}. Contrato: {{contrato}}; POP {{pop}}, OLT {{olt}}, PON {{pon}}, ONU {{onu_serial}}; última vez online: {{ultima_vez_online}}; LOS: {{led_los}}; PON: {{led_pon}}; energia: {{energia}}. HIPÓTESE (não é fato): {{hipotese}}.").
		From("hip", "false").Condition("hip2", "led_los", "eq", "aceso").
		From("hip2", "true").Set("h_optico", "hipotese", "possível problema óptico, LOS aceso (hipótese, sem confirmação)").Connect("h_optico", "next", "tk").
		From("hip2", "false").Set("h_none", "hipotese", "sem hipótese inicial").Connect("h_none", "next", "tk").
		Handoff("h_partial", "queue.noc", "Triagem de ONU incompleta. Contrato: {{contrato}}; ONU: {{onu_serial}}.").
		Connect("tk", "error", "h_partial").
		Var("categoria", "string", "").Var("hipotese", "string", "Hipótese inicial: nunca é declarada como fato")
	return ispTpl("isp-onu-offline", "ISP - ONU/ONT offline", "POP, OLT, PON, serial, LEDs e energia. Levanta hipóteses (óptico, energia) sempre marcadas como hipótese, nunca como fato.", b, []TestCase{
		{Name: "LOS aceso levanta hipótese óptica, marcada como hipótese", Scenario: Scenario{Events: Msg("oi", "João", "POP-1", "OLT-2", "PON-3", "ABCD1234", "ontem", "2", "1", "1")},
			Expect: Expect{Status: "waiting_human", Priority: "medium", Vars: map[string]string{"hipotese": "possível problema óptico, LOS aceso (hipótese, sem confirmação)"}}},
		{Name: "sem energia levanta hipótese de energia", Scenario: Scenario{Events: Msg("oi", "João", "POP-1", "OLT-2", "PON-3", "ABCD1234", "ontem", "1", "1", "2")},
			Expect: Expect{Status: "waiting_human", Vars: map[string]string{"hipotese": "possível problema de energia no local (hipótese, sem confirmação)"}}},
		{Name: "sem indício não inventa causa", Scenario: Scenario{Events: Msg("oi", "João", "POP-1", "OLT-2", "PON-3", "ABCD1234", "ontem", "1", "1", "1")},
			Expect: Expect{Status: "waiting_human", Vars: map[string]string{"hipotese": "sem hipótese inicial"}}},
	})
}

func tCollective() *Template {
	b := NewBuilder().Start().Set("cat", "categoria", "COLLECTIVE").FindTickets("rel").
		From("rel", "found").Say("linked", "Já existe um incidente aberto relacionado: \"{{tickets.first_subject}}\". Vamos anexar o seu caso a ele.").
		Handoff("h_linked", "queue.noc", "Possível incidente coletivo. Reclamante: {{contact.name}} {{customer.name}}. Já existe incidente aberto: {{tickets.first_subject}}. NENHUM chamado duplicado foi criado.").
		From("rel", "none").
		Steps("sev", "h_partial",
			ChoiceS{ID: "cenario", Text: "Qual parece ser o cenário?", Var: "cenario", Opts: []Opt{{"fibra", "Rompimento de fibra", "rompimento de fibra"}, {"pop", "Queda de POP", "queda de POP"}, {"pon", "Queda de PON", "queda de PON"}, {"backbone", "Falha de backbone", "falha de backbone"}, {"upstream", "Falha de operadora upstream", "falha de upstream"}, {"outro", "Outro", "outro"}}},
			AskS{ID: "regiao", Text: "Qual é a região/POP afetada?", Var: "regiao"},
			AskS{ID: "afetados", Text: "Quantos clientes ou locais estão afetados? (número)", Var: "afetados", Validation: "number"}).
		Condition("sev", "afetados", "gte", 50).
		From("sev", "true").Ticket("t_crit", "Incidente coletivo - {{regiao}} ({{cenario}})", "critical").
		Handoff("h", "queue.noc", "{{categoria}}: {{cenario}} em {{regiao}}; afetados: {{afetados}}. Severidade pela regra: 50 ou mais afetados = crítico; 10 a 49 = alta.").
		From("sev", "false").Condition("sev2", "afetados", "gte", 10).
		From("sev2", "true").Ticket("t_high", "Incidente coletivo - {{regiao}} ({{cenario}})", "high").Connect("t_high", "next", "h").
		From("sev2", "false").Ticket("t_med", "Incidente coletivo - {{regiao}} ({{cenario}})", "medium").Connect("t_med", "next", "h").
		Handoff("h_partial", "queue.noc", "Triagem de incidente coletivo incompleta. Região: {{regiao}}; cenário: {{cenario}}.").
		Connect("t_crit", "error", "h_partial").Connect("t_high", "error", "h_partial").Connect("t_med", "error", "h_partial").
		Var("categoria", "string", "")
	return ispTpl("isp-collective-incident", "ISP/NOC - Incidente coletivo", "Se já há incidente aberto, anexa o caso sem duplicar chamado; senão classifica o cenário e a gravidade pelo número de afetados.", b, []TestCase{
		{Name: "incidente já aberto: não duplica", Scenario: Scenario{OpenTickets: 1, Events: Msg("oi")}, Expect: Expect{Status: "waiting_human", Say: []string{"Já existe um incidente aberto", "Link fora do ar"}, NotReaches: []string{"t_crit", "t_high", "t_med"}, Effects: []string{"handoff"}}},
		{Name: "50 ou mais afetados: crítico", Scenario: Scenario{Events: Msg("oi", "1", "Zona Sul", "120")}, Expect: Expect{Status: "waiting_human", Priority: "critical"}},
		{Name: "10 a 49: alta", Scenario: Scenario{Events: Msg("oi", "3", "Centro", "25")}, Expect: Expect{Status: "waiting_human", Priority: "high"}},
		{Name: "poucos: média", Scenario: Scenario{Events: Msg("oi", "6", "Norte", "3")}, Expect: Expect{Status: "waiting_human", Priority: "medium"}},
	})
}

func tCritical() *Template {
	b := NewBuilder().Start().Set("cat", "categoria", "CRITICAL_INCIDENT").
		Steps("i1", "h_partial",
			ChoiceS{ID: "critico", Text: "O serviço afetado é crítico para o cliente?", Var: "servico_critico", Opts: YesNo("Sim, é crítico", "Não")},
			AskS{ID: "afetados", Text: "Quantos usuários ou locais estão afetados? (número)", Var: "afetados", Validation: "number"},
			ChoiceS{ID: "interrupcao", Text: "A interrupção é total ou parcial?", Var: "interrupcao", Opts: []Opt{{"total", "Total", "total"}, {"parcial", "Parcial", "parcial"}}},
			ChoiceS{ID: "redundancia", Text: "Como está a redundância?", Var: "redundancia", Opts: []Opt{{"sem", "Não há redundância", "sem"}, {"degradada", "Há, mas está degradada", "degradada"}, {"operando", "Há e está operando", "operando"}}}).
		// CRITICAL: total outage AND no redundancy AND (critical service OR 50+ affected)
		// HIGH:     total outage (redundancy degraded/ok) OR critical service OR 50+ affected
		// MEDIUM:   everything else
		Condition("i1", "interrupcao", "eq", "total").
		From("i1", "true").Condition("i2", "redundancia", "eq", "sem").
		From("i2", "true").Condition("i3", "servico_critico", "eq", "sim").
		From("i3", "true").Ticket("t_crit", "Incidente crítico - {{customer.name}}", "critical").
		Handoff("h", "queue.noc", "{{categoria}}. Crítico: {{servico_critico}}; afetados: {{afetados}}; interrupção: {{interrupcao}}; redundância: {{redundancia}}. Severidade calculada por regras determinísticas, não por IA.").
		From("i3", "false").Condition("i4", "afetados", "gte", 50).
		From("i4", "true").Connect("i4", "true", "t_crit").
		From("i4", "false").Ticket("t_high", "Incidente grave - {{customer.name}}", "high").Connect("t_high", "next", "h").
		Connect("i2", "false", "t_high").
		From("i1", "false").Condition("j1", "servico_critico", "eq", "sim").
		Connect("j1", "true", "t_high").
		From("j1", "false").Condition("j2", "afetados", "gte", 50).
		Connect("j2", "true", "t_high").
		From("j2", "false").Ticket("t_med", "Incidente - {{customer.name}}", "medium").Connect("t_med", "next", "h").
		Handoff("h_partial", "queue.noc", "Triagem de incidente crítico incompleta.").
		Connect("t_crit", "error", "h_partial").Connect("t_high", "error", "h_partial").Connect("t_med", "error", "h_partial").
		Var("categoria", "string", "")
	return ispTpl("isp-critical-incident", "NOC - Incidente crítico", "Gravidade 100% determinística (serviço crítico, afetados, interrupção total, redundância). Uma IA pode sugerir, nunca decide crítico.", b, []TestCase{
		{Name: "total, sem redundância, serviço crítico: crítico", Scenario: Scenario{Events: Msg("oi", "1", "5", "1", "1")}, Expect: Expect{Status: "waiting_human", Priority: "critical"}},
		{Name: "total, sem redundância, 50+ afetados: crítico", Scenario: Scenario{Events: Msg("oi", "2", "80", "1", "1")}, Expect: Expect{Status: "waiting_human", Priority: "critical"}},
		{Name: "total, sem redundância, poucos e não crítico: alta", Scenario: Scenario{Events: Msg("oi", "2", "5", "1", "1")}, Expect: Expect{Status: "waiting_human", Priority: "high"}},
		{Name: "total com redundância operando: alta", Scenario: Scenario{Events: Msg("oi", "1", "5", "1", "3")}, Expect: Expect{Status: "waiting_human", Priority: "high"}},
		{Name: "parcial mas serviço crítico: alta", Scenario: Scenario{Events: Msg("oi", "1", "5", "2", "3")}, Expect: Expect{Status: "waiting_human", Priority: "high"}},
		{Name: "parcial, não crítico, poucos: média", Scenario: Scenario{Events: Msg("oi", "2", "5", "2", "3")}, Expect: Expect{Status: "waiting_human", Priority: "medium"}},
		{Name: "a urgência dita pelo contato não vira crítico sozinha", Scenario: Scenario{Events: Msg("oi", "2", "URGENTE!!!")}, Expect: Expect{Status: "waiting_input", WaitingAt: "afetados", NotReaches: []string{"t_crit", "t_high", "t_med"}}},
	})
}

func tFinancial() *Template {
	b := NewBuilder().Start().
		Steps("h", "h", ChoiceS{ID: "assunto", Text: "Qual é o assunto financeiro?", Var: "assunto_fin", Opts: []Opt{{"segunda_via", "Segunda via de boleto", "segunda via"}, {"nf", "Nota fiscal", "nota fiscal"}, {"pagamento", "Pagamento", "pagamento"}, {"divergencia", "Divergência de cobrança", "divergencia"}, {"cobranca", "Cobrança", "cobranca"}, {"contrato", "Contrato", "contrato"}, {"outro", "Outro", "outro"}}}).
		Handoff("h", "queue.finance", "Financeiro: {{assunto_fin}}. Contato: {{contact.name}} {{customer.name}}.")
	return mk("isp-financial", 1, "ISP - Financeiro", "Encaminha demandas financeiras à fila certa sem abrir incidente técnico.", domain.FlowTypeSubflow, []string{"ISP", "FINANCIAL"}, Settings{}, b, []TestCase{
		{Name: "vai direto para o financeiro, sem chamado técnico", Scenario: Scenario{Events: Msg("oi", "1")}, Expect: Expect{Status: "waiting_human", Effects: []string{"handoff"}, NotReaches: []string{"ticket"}, Vars: map[string]string{"assunto_fin": "segunda via"}}},
		{Name: "sem resposta também vai ao financeiro", Scenario: Scenario{Events: []Event{{Text: "oi"}, {Timeout: true}}}, Expect: Expect{Status: "waiting_human", Effects: []string{"handoff"}}},
	}, recommended("isp"), withFeatures([]string{"conversations"}, nil))
}

func tCommercial() *Template {
	b := NewBuilder().Start().
		Steps("h", "h",
			AskS{ID: "nome", Text: "Qual é o seu nome?", Var: "lead_nome"},
			AskS{ID: "empresa", Text: "Qual é a sua empresa?", Var: "lead_empresa"},
			AskS{ID: "cidade", Text: "Em qual cidade é o serviço?", Var: "lead_cidade"},
			AskS{ID: "servico", Text: "Qual serviço você procura?", Var: "lead_servico"},
			AskS{ID: "requisito", Text: "Algum requisito específico (velocidade, IP fixo, SLA)?", Var: "lead_requisito"},
			AskS{ID: "prazo", Text: "Qual é o prazo desejado?", Var: "lead_prazo"},
			AskS{ID: "tel", Text: "Qual é o melhor telefone para contato?", Var: "lead_telefone", Validation: "phone"},
			AskS{ID: "email", Text: "E o e-mail?", Var: "lead_email", Validation: "email"}).
		Handoff("h", "queue.commercial", "Lead comercial: {{lead_nome}} ({{lead_empresa}}), {{lead_cidade}}. Serviço: {{lead_servico}}; requisito: {{lead_requisito}}; prazo: {{lead_prazo}}; tel: {{lead_telefone}}; e-mail: {{lead_email}}. Encaminhado à fila comercial (não há módulo de CRM para criar oportunidade).")
	return mk("isp-commercial", 1, "ISP - Comercial", "Coleta os dados do lead e encaminha à fila comercial (sem criar oportunidade: não há CRM nesta versão).", domain.FlowTypeSubflow, []string{"ISP", "COMMERCIAL"}, Settings{}, b, []TestCase{
		{Name: "coleta o lead completo", Scenario: Scenario{Events: Msg("oi", "Ana", "Beta SA", "Porto Alegre", "Link dedicado", "100 Mbps", "30 dias", "(51) 99999-8888", "ana@beta.com")},
			Expect: Expect{Status: "waiting_human", Effects: []string{"handoff"}, Vars: map[string]string{"lead_email": "ana@beta.com", "lead_telefone": "51999998888"}}},
		{Name: "e-mail inválido é perguntado de novo", Scenario: Scenario{Events: Msg("oi", "Ana", "Beta SA", "POA", "Link", "100", "30", "51999998888", "não tenho")}, Expect: Expect{Status: "waiting_input", WaitingAt: "email"}},
		{Name: "abandono entrega o parcial ao comercial", Scenario: Scenario{Events: []Event{{Text: "oi"}, {Text: "Ana"}, {Timeout: true}}}, Expect: Expect{Status: "waiting_human", Effects: []string{"handoff"}}},
	}, recommended("isp"), withFeatures([]string{"conversations"}, nil))
}

func tISPReception() *Template {
	b := NewBuilder().Start().
		Say("hello", "Olá {{contact.name}}! Você está no atendimento.").
		Contact("who").
		From("who", "known").Sub("ctx", "customer-context").
		Choice("menu", "Como podemos ajudar?", "assunto", Opt{"tec", "Problema técnico / sem serviço", ""}, Opt{"fin", "Financeiro", ""}, Opt{"com", "Comercial", ""}, Opt{"out", "Outro assunto", ""}).
		From("who", "unknown").Sub("unk", "unknown-contact").Connect("unk", "next", "menu").
		From("menu", "tec").Sub("exist", "existing-ticket").
		Condition("rel", "related", "eq", "sim").
		From("rel", "true").Handoff("h_exist", "queue.noc", "Cliente {{contact.name}} fala de um chamado que já existe: {{tickets.first_subject}}.").
		From("rel", "false").Choice("tmenu", "Qual é o problema?", "problema",
		Opt{"link", "Sem internet / link fora", ""}, Opt{"perf", "Lentidão ou perda de pacotes", ""}, Opt{"interm", "Conexão intermitente", ""}, Opt{"bgp", "BGP / roteamento", ""}, Opt{"dns", "DNS", ""},
		Opt{"equip", "Equipamento offline", ""}, Opt{"onu", "ONU/ONT offline", ""}, Opt{"coletivo", "Vários clientes sem serviço", ""}, Opt{"critico", "Incidente grave", ""}).
		From("tmenu", "link").Sub("s_link", "isp-link-down").End("end_after", "informational").
		From("tmenu", "perf").Sub("s_perf", "isp-performance").Connect("s_perf", "next", "end_after").
		From("tmenu", "interm").Sub("s_interm", "isp-intermittency").Connect("s_interm", "next", "end_after").
		From("tmenu", "bgp").Sub("s_bgp", "isp-bgp-routing").Connect("s_bgp", "next", "end_after").
		From("tmenu", "dns").Sub("s_dns", "isp-dns").Connect("s_dns", "next", "end_after").
		From("tmenu", "equip").Sub("s_equip", "isp-equipment-offline").Connect("s_equip", "next", "end_after").
		From("tmenu", "onu").Sub("s_onu", "isp-onu-offline").Connect("s_onu", "next", "end_after").
		From("tmenu", "coletivo").Sub("s_col", "isp-collective-incident").Connect("s_col", "next", "end_after").
		From("tmenu", "critico").Sub("s_crit", "isp-critical-incident").Connect("s_crit", "next", "end_after").
		From("menu", "fin").Sub("s_fin", "isp-financial").Connect("s_fin", "next", "end_after").
		From("menu", "com").Sub("s_com", "isp-commercial").Connect("s_com", "next", "end_after").
		From("menu", "out").Handoff("h_fb", "queue.fallback", "Outro assunto. Contato: {{contact.name}}.").
		Connect("menu", "timeout", "h_fb").Connect("tmenu", "timeout", "h_fb").
		Var("assunto", "string", "").Var("problema", "string", "").Var("related", "string", "Definido por existing-ticket").
		Var("nome", "string", "").Var("empresa_informada", "string", "")
	return mk("isp-noc-reception", 1, "ISP NOC - Recepção", "Recepção de ISP: identifica o contato e a empresa, evita chamado duplicado e encaminha ao fluxo técnico certo (link, desempenho, BGP, DNS, ONU, incidente coletivo...).",
		domain.FlowTypeInbound, ispCats, Settings{Priority: 100, IsDefault: true}, b, []TestCase{
			{Name: "cliente com link fora e sem backup: chamado crítico", Scenario: Scenario{ContactKind: "customer", Companies: []string{"ACME"}, Events: Msg("oi", "1", "1", "POA-1", "Algar", "14:30", "1", "1", "1")},
				Expect: Expect{Status: "waiting_human", Reaches: []string{"s_link", "t_crit"}, Priority: "critical", Effects: []string{"company_validated", "ticket", "handoff"}}},
			{Name: "cliente com chamado aberto não duplica", Scenario: Scenario{ContactKind: "customer", Companies: []string{"ACME"}, OpenTickets: 1, Events: Msg("oi", "1", "1")},
				Expect: Expect{Status: "waiting_human", Reaches: []string{"h_exist"}, NotReaches: []string{"s_link"}, Effects: []string{"company_validated", "handoff"}}},
			{Name: "financeiro", Scenario: Scenario{ContactKind: "customer", Companies: []string{"ACME"}, Events: Msg("oi", "2", "1")}, Expect: Expect{Status: "waiting_human", Reaches: []string{"s_fin"}, NotReaches: []string{"ticket"}}},
			{Name: "contato desconhecido pede comercial", Scenario: Scenario{ContactKind: "unclassified", Events: Msg("oi", "Pedro", "Beta", "3", "Pedro", "Beta SA", "POA", "Link", "100", "30", "51999998888", "p@beta.com")},
				Expect: Expect{Status: "waiting_human", Reaches: []string{"unk", "s_com"}, NotReaches: []string{"ctx"}, Vars: map[string]string{"lead_email": "p@beta.com"}}},
			{Name: "vários clientes sem serviço segue o fluxo coletivo", Scenario: Scenario{ContactKind: "customer", Companies: []string{"ACME"}, Events: Msg("oi", "1", "8")}, Expect: Expect{Status: "waiting_input", Reaches: []string{"s_col"}, WaitingAt: "cenario"}},
			{Name: "incidente coletivo já aberto: anexa sem duplicar", Scenario: Scenario{ContactKind: "customer", Companies: []string{"ACME"}, OpenTickets: 1, Events: Msg("oi", "1", "2", "8")},
				Expect: Expect{Status: "waiting_human", Say: []string{"Já existe um incidente aberto"}, NotReaches: []string{"t_crit", "t_high", "t_med"}, Effects: []string{"company_validated", "handoff"}}},
			{Name: "sem resposta no menu: triagem humana", Scenario: Scenario{ContactKind: "customer", Companies: []string{"ACME"}, Events: []Event{{Text: "oi"}, {Timeout: true}}}, Expect: Expect{Status: "waiting_human", Reaches: []string{"h_fb"}}},
		}, recommended("isp"), intermediate(), withFeatures([]string{"conversations", "ticketing"}, []string{"monitoring"}))
}
