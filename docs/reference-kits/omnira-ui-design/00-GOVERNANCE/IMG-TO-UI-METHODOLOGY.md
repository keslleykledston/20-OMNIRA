# Metodologia OMNIRA Img → UI

Metodologia adaptada da ideia de decomposição da skill `img-to-html`: usar a imagem como contrato visual, decompor antes de codificar, construir por camadas e comparar screenshots. O destino OMNIRA é React/Next.js, não HTML estático.

## Pipeline obrigatório

### 0. Reuse audit

Antes de JSX:

- procurar AppShell, Button, Card, Badge, Tabs, Table, Sheet, Modal, SearchField etc.;
- classificar cada necessidade como `REUSE`, `EXTEND` ou `CREATE`;
- não criar segunda abstração de design system.

### 1. Reference analysis

Para cada imagem identificar:

- viewport e proporção;
- regiões principais;
- grid e alinhamentos;
- hierarquia tipográfica;
- densidade;
- cores semânticas;
- radius, borda, sombra;
- componentes repetidos;
- pistas de interação.

### 2. Wireframe tipado

Criar um wireframe ASCII com regiões e relações. O wireframe serve como contrato estrutural; não precisa reproduzir pixels.

### 3. Tokens

Mapear valores visuais para tokens semânticos existentes. Se faltar token, estender o design system antes de usar magic value na feature.

### 4. Component map

Tabela `região → componente → REUSE/EXTEND/CREATE`. Só depois implementar.

### 5. Foundation first

Primitives/patterns faltantes entram no design system; componentes específicos ficam na feature.

### 6. Page implementation

Página compõe componentes, usa data adapter e estados explícitos. Nada de `page.tsx` monolítico.

### 7. Screenshot comparison

Renderizar no mesmo viewport da referência. Comparar primeiro macro-layout e só depois detalhes.

Ordem de correção:
1. estrutura/proporções;
2. spacing;
3. tipografia;
4. cores/superfícies;
5. radius/border/shadow;
6. ícones/assets.

### 8. Responsive + accessibility

Depois do desktop fiel: tablet, mobile, teclado, focus, labels, contraste e touch targets.

### 9. Review gate

Rodar `ui-ios-review` ou equivalente existente. Uma tela só passa após os gates em `ACCEPTANCE-GATES.md`.

## Observação sobre a skill externa

A skill externa pode ser usada como referência de processo, mas não deve ser vendorizada sem confirmação de licença/autorização. Este pacote contém uma implementação metodológica própria para OMNIRA.
