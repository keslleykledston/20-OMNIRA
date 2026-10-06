package templates

func allPacks() []*Pack {
	return []*Pack{
		{Slug: "omnira-starter", Version: 1, Name: "OMNIRA Starter Pack", Description: "Recepção inteligente, contato desconhecido, contexto de empresa, chamado existente, transferência para humano, fora do horário e CSAT.",
			Categories: []string{"GENERAL", "CUSTOMER_SERVICE"}, RecommendedFor: []string{"general", "isp", "msp"},
			Items: []PackItem{{"smart-reception", 1, false}, {"unknown-contact", 1, false}, {"customer-context", 1, false}, {"existing-ticket", 1, false}, {"human-handoff", 1, false}, {"after-hours", 1, true}, {"csat", 1, true}}},
		{Slug: "k3g-support", Version: 1, Name: "K3G Support Pack", Description: "Atendimento de empresa de TI e redes: horário comercial, VPN, firewall, mudança e triagem técnica geral (sem executar mudanças automáticas).",
			Categories: []string{"K3G", "TECHNICAL_SUPPORT", "NETWORK"}, RecommendedFor: []string{"msp"},
			Items: []PackItem{{"k3g-central-reception", 1, false}, {"k3g-vpn-support", 1, false}, {"k3g-firewall-support", 1, false}, {"k3g-change-request", 1, false}, {"k3g-general-triage", 1, false},
				{"unknown-contact", 1, false}, {"customer-context", 1, false}, {"after-hours", 1, false}, {"human-handoff", 1, true}, {"csat", 1, true}}},
		{Slug: "isp-noc", Version: 1, Name: "ISP NOC Pack", Description: "Atendimento de provedor e NOC: link fora, desempenho, intermitência, BGP/roteamento, DNS, equipamento, ONU/ONT, incidente coletivo e crítico, financeiro e comercial. Severidade só por regras determinísticas.",
			Categories: []string{"ISP", "NOC", "NETWORK"}, RecommendedFor: []string{"isp"},
			Items: []PackItem{{"isp-noc-reception", 1, false}, {"isp-link-down", 1, false}, {"isp-performance", 1, false}, {"isp-intermittency", 1, false}, {"isp-bgp-routing", 1, false}, {"isp-dns", 1, false},
				{"isp-equipment-offline", 1, false}, {"isp-onu-offline", 1, false}, {"isp-collective-incident", 1, false}, {"isp-critical-incident", 1, false}, {"isp-financial", 1, false}, {"isp-commercial", 1, false},
				{"unknown-contact", 1, false}, {"customer-context", 1, false}, {"existing-ticket", 1, false}, {"human-handoff", 1, true}, {"csat", 1, true}}},
	}
}
