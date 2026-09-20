# Omnira iOS Design System

## Linguagem visual

Aplicação SaaS operacional inspirada em princípios iOS: clareza, hierarquia, controles previsíveis, grandes superfícies claras, cantos arredondados e feedback sutil. Não copiar assets Apple.

## Tokens baseline

> Valores são baseline visual e devem ser centralizados. Se o projeto já possui tokens aprovados, preservar os existentes e ajustar apenas quando necessário para bater a referência.

| Token | Baseline | Uso |
|---|---:|---|
| `surface.canvas` | `#F6F8FB` | background geral |
| `surface.primary` | `#FFFFFF` | cards/panels |
| `surface.muted` | `#F9FAFB` | áreas secundárias |
| `border.subtle` | `#E5EAF0` | hairlines |
| `text.primary` | `#101828` | títulos/texto principal |
| `text.secondary` | `#667085` | descrição/metadado |
| `text.tertiary` | `#98A2B3` | hint/placeholder |
| `accent.primary` | `#1683FF` | ação/seleção |
| `status.success` | `#12B76A` | sucesso/conectado |
| `status.warning` | `#F79009` | espera/atenção |
| `status.danger` | `#F04438` | erro/alta prioridade |
| `status.info` | `#2E90FA` | informação |

## Spacing

Escala: `4, 8, 12, 16, 20, 24, 32, 40, 48` px.

- gap entre ícone e label: 8–12;
- padding de card compacto: 16;
- card normal: 20–24;
- seção entre blocos: 24–32;
- page padding desktop: 24–32.

## Radius

- controles: 8–10 px;
- cards: 12–14 px;
- sheets/modals: 18–20 px;
- pills/badges: full/999.

## Shadow

Usar sombra mínima; a separação deve vir principalmente de surface + border. Evitar shadow pesada.

## Tipografia

Stack: `-apple-system, BlinkMacSystemFont, Inter, "Segoe UI", sans-serif`.

Hierarquia sugerida:

- page title: 28–32 / 34–38, 650–700;
- section title: 16–18 / 22–24, 600–650;
- card metric: 24–30 / 30–34, 700;
- body: 14–16 / 20–24, 400–500;
- metadata: 12–13 / 16–18, 400–500.

## Iconografia

Uma família única de outline icons. Tamanho base: 18–20; ações compactas 16; feature tiles 24–28.

## Badges

Fundos suaves com texto/ícone semântico. Nunca usar saturação forte em grandes áreas.

## Form controls

Altura desktop: 36–40 px. Inputs grandes do wizard podem chegar a 44 px. Focus ring azul claro + outline visível.
