# Skill: omnira-img-to-ui

Metodologia controlada de decomposição de mockups visuais em componentes React/TypeScript reutilizáveis.

**Baseada em**: Decomposição conceptual da skill pública `img-to-html`, adaptada para OMNIRA design system.

**Não copia**: Implementação externa; usa apenas pipeline conceitual.

## Pipeline obrigatório

```
REFERENCE (PNG mockup)
  ↓
REUSE AUDIT (REUSE → EXTEND → CREATE)
  ↓
WIREFRAME (ASCII tipado com regiões)
  ↓
TOKEN MAP (cores, spacing, radius → tokens semânticos)
  ↓
COMPONENT MAP (região → componente → status)
  ↓
IMPLEMENTATION (React/TypeScript)
  ↓
SCREENSHOT (mesmo viewport da referência)
  ↓
VISUAL DIFF (P0/P1/P2/P3)
  ↓
RESPONSIVE (tablet, mobile)
  ↓
ACCESSIBILITY (WCAG AA, keyboard, focus)
  ↓
HUMAN GATE (aprovação visual antes de próxima tela)
```

## Regra obrigatória

**Não avançar para a próxima tela antes da aprovação visual humana do GATE.**

Cada tela segue ciclo completo: reference → implementation → screenshot → diff → PARE.

## Artefatos de review

- **Referência canônica**: `docs/reference-kits/omnira-ui-design/references/` — source of truth visual.
- **Evidência de implementação**: `docs/design/review/<wave>/` — versionar apenas o screenshot
  final aprovado de cada wave, quando servir de baseline de regressão. Capturas
  intermediárias ou reprovadas não vão para o Git.

## Reuse matrix template

| Região | Componente | Status | Motivo |
|--------|-----------|--------|--------|
| Header | PageHeader | REUSE/EXTEND/CREATE | ... |
| Sidebar | AppSidebar | EXTEND | ... |
| Card item | ConversationRow | CREATE | ... |

## Design System canonical

Fonte canônica: `docs/reference-kits/omnira-ui-design/`

- governance: `00-GOVERNANCE/`
- foundation: `01-FOUNDATION/`
- screen specs: `02-SCREENS/`
- imagens de referência: `references/`

Tokens já congelados em:
- `web/src/design/tokens.css` (CSS variables)
- `web/src/design/typography.css` (font hierarchy)

Primitives implementados em:
- `web/src/components/primitives/` (Button, Card, Badge, etc.)

## Não fazer

- Criar componente duplicado sem verificar REUSE.
- Implementar magic CSS values em telas (use tokens).
- Avançar para próxima tela sem screenshot + human approval.
- Refatorar backend, auth ou contrato de API.
- Espalhar design tokens por telas (centralize em tokens.css).

## Documentação

Após cada tela:

1. Listar reuse/extend/create com justificativa.
2. Screenshot comparison (REFERENCE vs IMPLEMENTATION).
3. P0/P1/P2/P3 divergências.
4. PAUSE para gate humano.
