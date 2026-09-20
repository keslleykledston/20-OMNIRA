# 13 SETTINGS QUEUES SLA

**Rota:** `/app/settings/queues + /app/settings/sla`  
**Referência visual:** `../references/13-settings-queues-sla.png`  
**Papéis:** Admin; Supervisor se delegado.

## Propósito

Configurar filas, capacidade, estratégia de routing e políticas de SLA globais/por fila.

## Wireframe

```text
APP SHELL
└─ SETTINGS
   ├─ Settings nav
   └─ Content
      ├─ Queues section/table
      ├─ SLA global cards
      └─ SLA by queue table
```

## Layout e regiões

- A referência mostra Filas e SLA no mesmo viewport; implementação pode separar rotas mantendo componentes e composição iguais.
- Queue rows: ícone, nome/descrição, avatar group, capacidade, estratégia, status, actions.
- SLA global usa 3 cards: primeira resposta, espera máxima, resolução.
- SLA por fila em tabela com selects compactos.

## Funcionalidades e interações

Criar/editar fila, membros, capacidade e estratégia (manual, round robin, capacity/availability conforme backend).  
Ativar/desativar fila.  
Salvar SLA global e override por fila.  
Toggle `usar configuração global`.  
Restaurar padrões exige confirmação.

## Dados necessários

Queue: id, name, description, members, capacity, strategy, status.  
SLA: firstResponseDuration, maxWaitDuration, resolutionDuration, scope/global/queue, enabled.  
Não inventar estratégias não suportadas pelo backend.

## Estados obrigatórios

Loading, empty queues, validation de duration/capacity, save progress, conflict, permission. Desativar fila com tickets ativos deve apresentar consequência/validação backend.

## Responsividade

Tablet: queue cards ou tabela reduzida; SLA global empilha; mobile usa forms por seção e member picker em Sheet.

## Component map conceitual

`SettingsNav`, `QueueRow`, `AvatarGroup`, `Select`, `StatusBadge`, `SLAConfigCard`, `SLATable`, `Switch`, `Button`.

## Checklist de fidelidade

- filas e SLA usam mesma hierarquia do mock;
- controles compactos e alinhados;
- avatars pequenos com `+N`;
- cards SLA com ícones coloridos suaves;
- salvar alterações bem visível.
