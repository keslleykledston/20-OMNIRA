# Gates de aceitação visual e funcional

Uma tela só recebe PASS quando todos os itens relevantes estiverem atendidos.

## Design

- [ ] usa AppShell compartilhado;
- [ ] usa tokens semânticos;
- [ ] primitives não duplicados;
- [ ] spacing consistente com escala;
- [ ] tipografia consistente;
- [ ] bordas e sombras discretas;
- [ ] tenant ativo visível quando aplicável;
- [ ] ícones de uma única família.

## Estados

- [ ] loading com skeleton apropriado;
- [ ] empty state;
- [ ] error state com retry quando aplicável;
- [ ] permission denied;
- [ ] degraded/offline para canais/realtime quando aplicável.

## Interação

- [ ] hover/focus/pressed/disabled;
- [ ] keyboard navigation;
- [ ] focus management em modal/sheet;
- [ ] operações perigosas exigem confirmação;
- [ ] ações indisponíveis explicam por quê.

## Responsividade

- [ ] desktop ≥1280;
- [ ] tablet 768–1279;
- [ ] mobile <768;
- [ ] sem overflow horizontal indevido;
- [ ] tabelas adaptam ou viram cards/scroll controlado.

## Visual comparison

- [ ] screenshot no viewport de referência;
- [ ] macro layout equivalente;
- [ ] densidade equivalente;
- [ ] componentes principais equivalentes;
- [ ] diferenças inevitáveis documentadas.

## Dados

- [ ] fixtures ficam fora do componente;
- [ ] API real usada quando endpoint existe;
- [ ] nenhum secret no browser;
- [ ] RBAC não depende apenas da UI.
