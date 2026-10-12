# Contrato de layout do Hub (aprovado) — o que as próximas fases de divisão de tenant não podem quebrar

Status: ativo desde 2026-10-12. Dono: o dono do produto. Mudar uma regra desta página exige decisão dele (e atualização dos testes citados), não uma edição silenciosa.

## 1. De onde vem

| Fonte | O que contribui |
|---|---|
| `.agent/multitenant-service-hub/PRD.md` §7 (Agent Workspace UI) e MT-AT-009 | Caixa unificada só das minhas empresas; barra de identidade da empresa; **a empresa é reconhecida por logo + código + nome, nunca só por cor** |
| ADR-0033 (shell estável + identidade do tenant) | O shell não muda ao trocar de empresa; rascunho e composer qualificados por (empresa, conversa); confirmação de ação sensível mostra a empresa |
| ADR-0036 + `LOVABLE_INTEGRATION_MATRIX.md` | O OMNIRA é a aplicação; o protótipo é **referência**. PRESERVE > EXTEND > REFACTOR LOCALLY > REPLACE |
| Protótipo Lovable `HubWorkspace.tsx` (commit `f3b83f9`, "Redesenhou layout do Hub") | Três painéis: fila (336–360) · conversa · contexto do cliente; menu à esquerda recolhível; filtros e busca; cliente/empresa em cada linha |
| Protótipo Lovable `HubMultiTenantPreview.tsx` (commit `158d710`, "Simulou abas de tenant", 2026-10-12) | **Abas de empresa** com contador (Todos os tenants · Empresa A · Empresa B); faixa "Contexto ativo"; **abas de atendimentos abertos** com fechar sem encerrar |

O protótipo é lido pelo MCP do Lovable em modo leitura. Nada dele é copiado: não entram o roteador, o estado, os dados falsos, as dependências (TanStack, shadcn, Tailwind 4) nem a paleta roxa (`.hub-theme`). Entram **estrutura e comportamento**, refeitos sobre os componentes e tokens do OMNIRA.

## 2. As regiões aprovadas (em ordem, de cima para baixo e da esquerda para a direita)

```
┌ Shell OMNIRA (Layout/Header/Sidebar) — NÃO substituído ─────────────────────────┐
│ ▢ menu à esquerda, recolhível para ícones (lembrado por pessoa)                   │
│ ┌ Abas de instância ────────────────────────────────────────────────────────────┐ │
│ │ Todas (n) · Empresa A (n) · Empresa B (n)     n = esperando resposta           │ │
│ ├ Faixa: ◈ Caixa: Todas as instâncias | Contexto ativo: <empresa>   · N conversas│ │
│ ├──────────────┬─────────────────────────────────────┬──────────────────────────┤ │
│ │ FILA         │ Atendimentos abertos (abas ✕)       │ DETALHES DO ATENDIMENTO  │ │
│ │ Em andamento │ ─────────────────────────────────── │ (recolhíveis)            │ │
│ │  | Encerradas│ Cabeçalho: cliente · EMPRESA · canal│ ▸ Atendimento            │ │
│ │ busca        │ mensagens                           │ ▸ Cliente                │ │
│ │ Todas·Pend.· │ composer ("Respondendo como X")     │ ▸ Linha do tempo         │ │
│ │ Minhas       │                                     │ ▸ Ações                  │ │
│ └──────────────┴─────────────────────────────────────┴──────────────────────────┘ │
└────────────────────────────────────────────────────────────────────────────────────┘
```

Invariantes (cada um tem teste; ver §5):

1. **A fila de atendimentos pendentes é separada do histórico.** Duas abas de escopo ("Em andamento" / "Encerradas"); dentro de "Em andamento", "Todas · Pendentes · Minhas". O histórico não tem filtro de fila. O filtro é pedido ao servidor (`segment`), nunca feito sobre uma lista parcial no navegador.
2. **Três colunas** ≥ 1440 px (fila 360 · conversa · detalhes 320); abaixo disso os detalhes abrem em gaveta pelo mesmo botão do cabeçalho da conversa; no celular, lista → conversa com "voltar". A regra de 1440 px é a mesma da caixa da empresa (`InboxWorkspace`).
3. **Os detalhes do atendimento são seções recolhíveis** (`CollapsibleSection`, a mesma da caixa da empresa), com a escolha lembrada por pessoa. Na caixa completa da instância (delegada) todas as seções começam abertas: nada que o agente já alcançava fica atrás de um clique.
4. **A empresa é sempre dita, por extenso**: em cada linha, no cabeçalho da conversa, na faixa "Contexto ativo", em cada aba de atendimento e no painel de detalhes (`TenantBadge`: código + nome completo; nunca só cor).
5. **Abas de instância** trazem o contador do que espera resposta (soma em "Todas"); sem número quando nada espera.
6. **Atendimentos abertos como abas**: abrir uma conversa a adiciona; clicar numa aba a traz de volta; **fechar a aba só a esquece** (não finaliza, não transfere, não escreve nada); uma conversa que deixou de poder ser aberta (acesso encerrado) sai das abas; limite de 8; pertencem a um Hub só.
7. **O menu da esquerda recolhe para ícones** e lembra a escolha; abaixo de `xl` ele já é só ícones.
8. **O shell não é trocado**: `Layout`, `Header`, `Sidebar`, `MobileNav` continuam sendo os do OMNIRA. Não existe um "app dentro do app" do Hub.

## 3. Regras para as próximas fases de divisão de tenant

A divisão de tenant (quem enxerga o quê, por contrato/concessão/fila) muda **o que enche** estas regiões, nunca **quais regiões existem**.

| Se a fase… | …então |
|---|---|
| criar uma nova **chave de permissão** (`x.y`) | a UI a oferece **dentro de uma seção existente** dos detalhes (ou como uma nova seção recolhível, aberta por padrão) e só quando o servidor a devolveu em `/me/access`. Nada de novo painel nem nova coluna |
| criar um novo **escopo** (fila, equipe, contrato) | entra no servidor (RLS e filtro do `segment`/`companies`); a tela continua pedindo as mesmas abas. O que a pessoa não pode ver simplesmente não vem; a tela não decide |
| criar uma nova **fonte de dados do cliente** (ERP, CRM, IA) | vira seção recolhível em **Detalhes** da caixa completa da instância (já é assim: Contato, Chamado, Resumo com IA, Anotações, Histórico). Na aba "Todas" só aparece o que o **item do Hub** carrega; o resto abre pelo botão "Abrir na caixa completa" |
| precisar de mais **uma região** (alertas, supervisão) | é uma **rota nova** (como `/supervisor`), não uma quarta coluna na caixa |
| tocar no **contador** das abas | o número vem de `companies[].unanswered` (servidor, sob a sessão da pessoa); nunca é somado a partir de listas no navegador |
| mexer em **cores/tokens** | só a camada `--tenant-accent`; a paleta OMNIRA fica (a roxa do protótipo é rejeitada) |
| mexer no **shell** (`Layout`, `Sidebar`…) | não: é decisão do dono (ADR-0036 item 5) |

Nunca: usar `tenant_id` de payload como autorização; ler dados de uma empresa com o tenant da sessão dentro da aba "Todas"; reutilizar `ChatPane`/`ContextPane` na lista agregada (leem pelo tenant da sessão); segredo em fila; mock como domínio.

## 4. O que foi adotado do protótipo e o que não

| Elemento do protótipo | Decisão | Onde |
|---|---|---|
| Abas "Todos os tenants / Empresa A / B" com contador | **ADAPT — feito** | `ConversationsEntry` + `Tabs` (`count`); número = `companies[].unanswered` |
| Faixa "Contexto ativo: Empresa · Operação / N conversas" | **ADAPT — feito** (na aba "Todas") | `HubInboxPage`. "Operação" (subdivisão da empresa) não existe no modelo: só a instância |
| "Atendimentos abertos" (abas com ✕) | **ADAPT — feito** | `HubOpenTabs`; fica só na memória da tela (não persiste entre recargas) |
| Painel "Contexto do cliente" (dados, histórico de interações, ações rápidas) | **ADAPT — feito** com o que o item carrega | `HubContextPane`. Telefone e e-mail **não** estão na projeção do Hub: ficam na caixa completa |
| Menu à esquerda recolhível | **ADAPT — feito** no Sidebar do OMNIRA | `Sidebar` (`omnira.sidebar.collapsed`) |
| Filtros "Novas / Lidas / Respondidas" | **NÃO adotado**: a caixa da empresa já usa Todas/Aguardando/Minhas/Encerradas e o Hub segue o mesmo vocabulário ("Pendentes" = sem responsável) | — |
| Seção "Inbox" separada de "Conversas" no menu | **NÃO adotado** como itens do menu (um único "Conversas", ADR-0039); a separação é fila × histórico dentro da caixa | — |
| Contador no item "Inbox" do menu | **PENDENTE** (exige consulta periódica fora da tela de conversas) | — |
| Mídia, status de entrega, tempo real, estado de conexão na aba "Todas" | **PENDENTE** (dependem de leitura de mídia por item do Hub e de SSE do Hub) | caixa completa da instância já tem |
| Encerrar conversa / copiar telefone (ações rápidas) | Encerrar existe na caixa completa (chave `conversation.finalize`); copiar telefone não, pelo motivo do telefone | — |
| `HubWorkspace.tsx` como componente | **REJEITADO** (ADR-0036): mock, estado e tema próprios | — |

## 5. Como o contrato é guardado

- `web/src/__tests__/HubLayoutContract.test.tsx` fixa a estrutura (regiões, abas, ordem, recolher, gaveta, faixa, abas de atendimento, menu recolhível, seções recolhíveis da caixa delegada).
- `web/src/__tests__/ConversationsTabs.test.tsx`: contador nas abas de instância.
- `internal/hub/adapters/inbox_segments_integration_test.go` (Postgres real, papel `omnira_app`, RLS): segmentos, busca, contadores; o que a pessoa não pode ler não é listado nem contado.
- `web/e2e-real/hub.real.spec.ts` ("layout aprovado…"): navegador real contra a API e o banco reais.

Se um destes testes falhar numa fase futura, **a fase mudou o layout aprovado**: pare e leve ao dono.
