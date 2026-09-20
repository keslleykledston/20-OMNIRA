# Catálogo de componentes

## Foundation

`AppShell`, `Sidebar`, `MobileNavigation`, `PageHeader`, `SectionHeader`, `Card`, `Button`, `IconButton`, `Avatar`, `Badge`, `StatusBadge`, `PriorityBadge`, `ChannelBadge`, `Input`, `TextArea`, `SearchField`, `Select`, `Combobox`, `Checkbox`, `Switch`, `Tabs`, `SegmentedControl`, `DropdownMenu`, `ContextMenu`, `Popover`, `Tooltip`, `Modal`, `Sheet`, `Drawer`, `Toast`, `Skeleton`, `EmptyState`, `ErrorState`, `PermissionState`, `Table`, `Pagination`, `FilterBar`, `DateRangePicker`, `Divider`, `Timeline`.

## Domain patterns

- `MetricCard`: ícone, label, value, trend, comparison label.
- `ConversationRow`: avatar, nome, canal, preview, hora, unread, selected.
- `MessageBubble`: direction, body, timestamp, delivery state, media.
- `MessageComposer`: tabs, textarea, actions, send state.
- `ContactCard`: identidade + canais + metadata.
- `TicketCard`/`TicketRow`: id, assunto, status, prioridade, contato.
- `ChannelCard`: provider, providerType, status, health, actions.
- `AutomationCard`/`AutomationRow`.
- `SLAIndicator`.
- `IntegrationCard`.

## Regra de component map

Toda screen spec traz um map conceitual. O agente deve reconciliar com o repo e marcar `REUSE/EXTEND/CREATE` antes de implementar.
