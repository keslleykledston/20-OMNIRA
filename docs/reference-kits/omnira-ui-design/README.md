# OMNIRA UI Design Specifications v1.0

Pacote canônico de especificação visual e funcional para reconstrução do frontend OMNIRA em **Next.js + TypeScript**, seguindo o **Omnira iOS Design System** e as referências visuais aprovadas.

## Objetivo

Este pacote converte os mockups em contratos implementáveis. Cada tela contém: propósito, rota, papéis, referência visual, wireframe estrutural, regiões, medidas aproximadas, componentes, dados, interações, estados, responsividade, acessibilidade e gates de fidelidade.

A metodologia é inspirada no fluxo de decomposição da skill pública `img-to-html` de rtadewald (imagem → wireframe → camadas → revisão visual), mas foi **adaptada** ao OMNIRA para produzir design system + componentes React/Next.js reutilizáveis. Este pacote não copia a implementação da skill e não exige que ela seja vendorizada.

## Ordem de leitura

1. `00-GOVERNANCE/CONTEXT-AND-RULES.md`
2. `00-GOVERNANCE/IMG-TO-UI-METHODOLOGY.md`
3. `01-FOUNDATION/DESIGN-SYSTEM.md`
4. `01-FOUNDATION/APP-SHELL.md`
5. `01-FOUNDATION/COMPONENT-CATALOG.md`
6. a spec da tela em `02-SCREENS/`
7. `03-IMPLEMENTATION/VISUAL-VALIDATION-PROCESS.md`
8. `03-IMPLEMENTATION/CODEX-EXECUTION-PROMPT.md`

## Referências visuais

As imagens aprovadas estão em `references/`. Elas são o padrão visual; as specs `.md` são o padrão funcional/técnico. Se houver conflito entre uma imagem e uma regra estrutural/segurança já aceita no projeto, a arquitetura e segurança vencem e a diferença deve ser documentada.

## Regra de implementação

**REUSE → EXTEND → CREATE.** Antes de criar um componente, verificar o que já existe em `packages/ui`, `apps/web`, `components`, `features` e `.agents/skills`.

Não construir um backend paralelo em Next.js. Dados de negócio vêm da API Go; fixtures são permitidas apenas por adapter substituível.
