# Prompt para Codex — reconstrução visual OMNIRA

Copie o bloco abaixo para o Codex na raiz do repositório.

```text
Você vai reconstruir o frontend OMNIRA usando o pacote de specs em:
OMNIRA-UI-DESIGN-SPECS-v1.0/
(ou o path onde este pacote foi copiado no repo).

POLÍTICA:
REUSE -> EXTEND -> CREATE.

Antes de codificar:
1. leia AGENTS.md e handoff/status existente;
2. valide git status/log;
3. audite skills/tools/frontend existentes;
4. leia README.md deste pacote;
5. leia DESIGN-SYSTEM.md, APP-SHELL.md, COMPONENT-CATALOG.md;
6. leia apenas a spec da wave/tela atual;
7. use a imagem correspondente em references/ como contrato visual.

NÃO crie segundo design system.
NÃO crie backend Next.js paralelo.
NÃO remova decisões existentes.

Use a metodologia de 00-GOVERNANCE/IMG-TO-UI-METHODOLOGY.md:
- reuse audit;
- reference analysis;
- wireframe;
- token map;
- component map;
- implementation;
- screenshot comparison;
- responsive/a11y gate.

Antes de implementar cada tela, reporte curto:
SCREEN:
REFERENCE:
REUSE:
EXTEND:
CREATE:
DATA SOURCE:
RISKS:

Depois implemente sem pedir confirmação, salvo blocker real.

Ordem:
F0 -> F1 -> F2 -> F3 -> F4 -> F5 -> F6 -> F7 -> F8 -> F9.

Uma wave por vez, commit verde por wave.

Ao final de cada wave:
- testes;
- screenshot comparison;
- responsive sanity;
- accessibility sanity;
- atualizar handoff existente;
- commit.

Reportar:
Wave:
Commit:
Screens:
Reused:
Extended:
Created:
Fixtures/API:
Tests:
Visual review:
Responsive:
Accessibility:
Known gaps:
Next:
```
