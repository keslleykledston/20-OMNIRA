# ADR-0036: Estratégia de evolução do frontend (o OMNIRA é a aplicação; protótipos são referência)

## Status
**Accepted** (2026-10-08, decisão do dono). A numeração 0025–0035 está reservada aos ADRs da missão do Service Hub (`.agent/multitenant-service-hub/ADRS-ESSENTIAL.md`), ainda não promovidos.

## Contexto
Em 2026-10-07 um protótipo gerado pelo Lovable foi tratado como se fosse o frontend do Hub: houve redirect de domínio para um deploy público do protótipo,
escrita em um diretório que a produção não serve e um componente órfão em `web/`. Nada foi substituído no repositório, mas o desvio mostrou o risco:
uma ferramenta de geração de UI produz uma **aplicação inteira** (roteador, auth, estado, modelo de dados falso) que pode ser confundida com o produto.
O OMNIRA já tem shell, rotas, sessão, clientes de API, primitives, tokens e testes em produção (`web/`).

## Decisão
1. **O OMNIRA atual é a fonte de verdade** arquitetural, funcional e operacional.
2. Protótipos gerados (Lovable, `omniflow-hub`, qualquer mock) são **referência de UX/UI**. Não são aplicação substituta, arquitetura alvo nem base de código.
3. **Nenhum protótipo pode virar a raiz da aplicação** sem um programa de reescrita futuro, aprovado separadamente e com ADR próprio.
4. Ordem de preferência para qualquer componente existente: **PRESERVE > EXTEND > REFACTOR LOCALLY > REPLACE**. `REPLACE` exige evidência de defeito.
5. Preservados salvo defeito comprovado e ADR: bootstrap, autenticação/sessão, `TenantContext`/`EffectiveTenantContext`, autorização, RLS, contratos e clientes de API, roteamento, estado, build, CI, primitives e tokens.
6. Evolução **aditiva, localizada, por adaptador e atrás de feature flag**. O Hub é uma superfície nova ao lado do workspace de tenant; não o substitui.
7. O frontend **nunca** é autoridade de tenant. O tenant selecionado na UI é contexto de exibição. O servidor resolve hub → contrato → grant → `EffectiveTenantContext`, e a RLS é a segunda barreira.
8. A inbox do Hub é uma **view agregada autorizada** (`GET /hubs/{id}/inbox`), nunca N chamadas por tenant montadas no navegador. O item é aberto pelo **id do item**; tenant e fila vêm da linha persistida.
9. Nada na UI chama ERP. O caminho é API OMNIRA → contexto efetivo → conector. A UI envia intenção, nunca credencial nem `integration_id` arbitrário.
10. Estado sensível no cliente é qualificado por contexto (`tenantId:conversationId`).
11. **Limites de mudança**: um PR de frontend que remove dezenas de módulos, troca framework/roteador/biblioteca de dados/auth/build ou recria `src/` é tratado como suspeito (parar e reavaliar). Antes de cada fatia declara-se arquivos esperados e compara-se com `git diff --stat`.
12. Antes de importar qualquer trecho gerado: remover dados falsos, estado e chamadas de API do protótipo, trocar por primitives e tokens do OMNIRA, ligar ao contrato real e **adicionar testes**.

## Consequências
Positivas: menor risco de regressão; contratos de domínio preservados; rollout gradual por flag; reaproveitamento alto; dono claro da arquitetura.
Custos: o ganho visual do protótipo chega por adaptação manual, mais lenta que copiar.
Operacional: `docs/ux/LOVABLE_INTEGRATION_MATRIX.md`, `COMPONENT_MAPPING.md` e `API_MAPPING.md` são pré-requisito de qualquer fatia visual do Hub.
Critério para começar mudança visual do Hub: provas POSTGRES/HTTP de acesso delegado, bloqueio cruzado e `EffectiveTenantContext` sem system-admin (`docs/architecture/HUB-VERIFICATION-STATUS.md`).

## Alternativas
- Adotar o protótipo como novo frontend: rejeitada (duplica auth/estado/roteamento, mock como domínio, reescrita sem rede de testes).
- Proibir qualquer geração de UI: rejeitada (o protótipo tem valor real em hierarquia, densidade e microinterações).
