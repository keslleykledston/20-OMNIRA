# ADR-0056: Ação sugerida no observador de grupo (texto livre, sem taxonomia fixa)

## Status
Aceito (2026-11-10), a pedido do dono do produto, depois de uma fase de análise manual sobre exports reais de
conversas de grupo/1:1 da própria K3G (ver `docs/research/group-tratativas-ia/mapeamento-v1.md`): estender o
observador da ADR-0055 para sugerir não só categoria/resumo, mas também uma ação provável — continuando
**só-sugestão, nunca execução** (decisão explícita de manter automação de verdade fora de escopo por ora).

## Contexto
A ADR-0055 implementou um observador de grupo que classifica cada mensagem em uma categoria fechada (`request`,
`complaint`, `question`, `info`, `other`) mais um resumo de uma linha. Analisando exports reais de WhatsApp da K3G
(grupo técnico multi-empresa + conversa 1:1 de 8 meses com um cliente), chegamos a uma taxonomia mais fina de 14
tipos de "tratativa" (pedido de anúncio BGP, escalonamento por SLA, manutenção agendada, cobrança de status, etc.) —
ver o documento de pesquisa citado acima.

O problema: essa taxonomia é do domínio de **NOC/ISP**, porque é assim que a K3G opera — não é genérica de
atendimento. O OMNIRA é multi-tenant; qualquer tabela ou `CHECK` de categoria que eu crave no schema fica
compartilhado por todo tenant que algum dia ligar grupos + observador, mesmo que seja uma clínica, um e-commerce ou
qualquer outro negócio sem nada a ver com BGP. Cravar a taxonomia A-N como enum no banco especializaria o produto
para o único tenant que o usa hoje.

## Decisão
1. **`suggested_action` é texto livre gerado pela IA, não um enum.** Sem lista fechada, sem `CHECK` de valores —
   igual ao `summary` que já existe. Nenhum jargão de ISP entra no schema.
2. **A taxonomia de tratativas é referência de prompt, não estrutura de dado.** As instruções fixas do gerador
   passam a pedir, além de categoria e resumo, uma ação sugerida curta (uma frase, até ~80 caracteres), com alguns
   exemplos do tipo de ação esperada (extraídos do documento de pesquisa) só para calibrar o estilo da resposta —
   nunca uma lista que a IA deva escolher. O corpo da mensagem continua sendo sempre dado de entrada, nunca
   instrução (mesma defesa contra prompt injection da ADR-0055).
3. **Mesma linha, novo campo.** `wa_group_message_suggestions` ganha `suggested_action TEXT NOT NULL DEFAULT ''`
   com `CHECK (length(suggested_action) <= 160)`, preenchido na mesma chamada de IA que já gera categoria/resumo
   (sem custo extra de latência ou chamada adicional). Idempotência, RLS e todo o resto do modelo da ADR-0055
   continuam sem mudança.
4. **Sem taxonomia configurável por tenant, por ora.** Avaliamos (e descartamos por ora) permitir que cada tenant
   configure seu próprio glossário de ações — mais correto a longo prazo, mas escopo bem maior (tela de
   configuração, nova tabela) do que "estender o observador atual". Fica registrado como alternativa futura, não
   como caminho aberto por este ADR.
5. **Continua só-sugestão.** Nada muda nas garantias da ADR-0055: nada é enviado ao grupo, nada aciona `SendText`,
   nada aparece em `internal/external`, nenhuma automação lê essa coluna para agir — só exibição na aba Grupos, para
   quem tem `group.read`.

## Consequências
- Ganho: sugestão mais útil para quem revisa (não só "é um pedido", mas "pedido de quê, provavelmente") sem
  acoplar o produto ao domínio de um único tenant.
- Custo: nenhum processo/tabela nova — é uma coluna a mais na mesma linha já gerada pela ADR-0055, mesmo prompt,
  mesma chamada de IA.
- **Tenancy:** sem mudança de superfície de tenancy; a coluna nova segue a mesma RLS já forçada na tabela.
- **Reversibilidade:** migration com `down` que remove a coluna; desligar o observador (geral ou por grupo)
  interrompe novas sugestões como já previsto na ADR-0055.
- **Risco aceito:** texto livre é mais difícil de filtrar/relatar que um enum (ex.: um dashboard de "quantos pedidos
  de anúncio BGP tivemos esse mês" não dá pra fazer com `GROUP BY` direto). Aceito conscientemente — se um tenant
  pedir relatório agregado por tipo de ação, isso é motivo para reabrir a decisão #4 (glossário configurável), não
  para forçar um enum genérico agora.

## Alternativas
- **Enum fechado com as 14 tratativas levantadas:** mais fácil de filtrar/reportar, mas especializa o schema para
  o jargão de ISP da K3G. Rejeitada — é o tipo de decisão de domínio que não deveria estar numa tabela
  compartilhada por todo tenant.
- **Glossário configurável por tenant:** mais correto a longo prazo, mas escopo bem maior que uma extensão do
  observador atual (tela de configuração, nova tabela de taxonomia por tenant). Fica como opção futura se o texto
  livre se mostrar insuficiente.

## Questões em aberto
- Se algum tenant precisar de relatório agregado por tipo de ação, decidir então entre NLP sobre o texto livre
  existente ou reabrir a decisão #4.
- Quando (e se) a ação sugerida vira um botão de "aplicar" com confirmação humana continua decisão futura, com ADR
  próprio — este ADR não abre esse caminho, igual ADR-0055 já deixava explícito.
