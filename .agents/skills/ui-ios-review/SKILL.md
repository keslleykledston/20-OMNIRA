# Skill: ui-ios-review

Revisão de conformidade visual com Omnira iOS Design System.

Executa APÓS implementação de tela para validar fidelidade, consistência e accessibility.

Não é construtora. É revisora.

## Checklist de conformidade

### Design System
- [ ] Todos os valores de cor usam tokens semânticos (não hardcoded hex)
- [ ] Espaçamento segue escala: 4, 8, 12, 16, 20, 24, 32, 40, 48
- [ ] Radius: 8–10 (controls), 12–14 (cards), 18–20 (sheets), full (pills)
- [ ] Shadows mínimas; separação via surface + border
- [ ] Font stack: -apple-system, BlinkMacSystemFont, Inter, Segoe UI
- [ ] Hierarquia tipográfica consistente

### Primitives
- [ ] Nenhum componente duplicado (Button, Card, Badge, etc.)
- [ ] Componentes reutilizáveis não espalhados por tela
- [ ] Props consistentes entre usos
- [ ] Estados (hover, active, disabled, loading) implementados

### Layout & Proporção
- [ ] Sidebar: 210–240px (desktop)
- [ ] Canvas background: surface.canvas (#F6F8FB aproximadamente)
- [ ] Superfícies: surface.primary (#FFFFFF)
- [ ] Borders: border.subtle (#E5EAF0 aproximadamente)
- [ ] Alignment: grid/flex coerente, não position:absolute sem necessidade
- [ ] Responsive: desktop, tablet, mobile testados visualmente

### Typography
- [ ] Tamanhos de fonte seguem hierarchy (não arbitrary)
- [ ] Line-height proporcionado
- [ ] Contraste: WCAG AA mínimo
- [ ] Sem dependência de apenas cor para significado

### Interactions
- [ ] Active state visível (especialmente navegação)
- [ ] Hover state sutil
- [ ] Disabled state claro
- [ ] Loading state: skeleton ou spinner contextual

### Accessibility
- [ ] Focus visible em todos os controles
- [ ] Teclado navegável (Tab, Enter, Escape)
- [ ] Labels acessíveis (aria-label ou label element)
- [ ] WCAG AA: contraste, touch targets, semantics
- [ ] Sem dependência de cor como único sinal
- [ ] Modals: focus trap + Escape close
- [ ] Tenant context sempre visível

### Mobile Responsive
- [ ] Desktop: 1440px+
- [ ] Tablet: 1024px
- [ ] Mobile: 390px
- [ ] Sem overflow horizontal
- [ ] Touch targets ≥44px
- [ ] Bottom nav (mobile) implementado

### Tenant Context
- [ ] Tenant ativo sempre visível
- [ ] Mudança de tenant clara (não invisível)
- [ ] Role do usuário visível

## Output

Gerar relatório: PASS / FAIL com lista de achados.

Se FAIL: relatar P0/P1/P2/P3 e sugerir remediação.

## Não fazer

- Consertar código (apenas reportar).
- Ignorar divergências "menores".
- Passar sem validação visual real (screenshots).
