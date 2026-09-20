# Settings — demais seções

Estas telas ainda não possuem mock visual dedicado; devem herdar **exatamente** o AppShell, SettingsNav, tokens e padrões das referências `12-settings-team.png` e `13-settings-queues-sla.png`.

## Geral — `/app/settings/general`

Campos: razão social, nome fantasia, CNPJ/tax ID, timezone, idioma, logo. Dividir em `Identidade`, `Localização` e `Preferências`. Save bar clara; validação inline.

## Integrações — `/app/settings/integrations`

Grid de `IntegrationCard`: IXC, SGP, HubSoft, MikWeb. Cada card mostra `Não configurado / Conectado / Erro`, última verificação e CTA. Secrets nunca reaparecem após cadastro.

## Segurança — `/app/settings/security`

Seções: sessões ativas, credenciais/integrations references, políticas. Não listar secrets em plaintext. Ações perigosas exigem reautenticação se política futura exigir.

## Auditoria — `/app/settings/audit`

Filter bar: ator, ação, recurso, período. Tabela: timestamp, ator, ação, resource, resultado. Detalhe abre Sheet. Logs são imutáveis na UI.

## Fidelidade

- mesmo subnav de Settings;
- cards/tables/inputs idênticos aos componentes já aprovados;
- não criar estilo novo para cada seção.
