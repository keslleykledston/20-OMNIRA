import type { IconName } from '../components/primitives/Icon'

// Only routes that exist in App.tsx. Spec items without a route (Automação, o
// restante de Configurações) stay hidden rather than simulated —
// docs/architecture/FRONTEND-UX.md. Equipe e acesso é a única seção de
// Configurações implementada até agora (IAM2A).
//
// mockBacked: destination has no real backend behind it (Relatórios —
// DESIGN.5 reality gate, FRONTEND.1). Filtered out of operational
// navigation outside development so it never implies active production
// capability; the route itself still exists (see UnavailableSurface).
// Supervisor became real in PRODUCT.1, Tickets in PRODUCT.2-B, Dashboard in
// PRODUCT.3-B — none of the three is contained anymore.
export type NavItem = { label: string; path: string; icon: IconName; group: NavGroup; alsoActiveOn?: string; mockBacked?: boolean; external?: boolean }
// UX audit M1: three groups with a small title, every item with its own icon (no repeats).
export type NavGroup = 'operacao' | 'automacao' | 'configuracao'
export const GROUP_TITLE: Record<NavGroup, string> = { operacao: 'Operação', automacao: 'Automação e canais', configuracao: 'Configuração' }
export const GROUP_ORDER: NavGroup[] = ['operacao', 'automacao', 'configuracao']

export const navItems: NavItem[] = [
  { label: 'Dashboard', path: '/', icon: 'dashboard', group: 'operacao' },
  { label: 'Conversas', path: '/inbox', icon: 'conversations', group: 'operacao' },
  { label: 'Tickets', path: '/tickets', icon: 'tickets', group: 'operacao' },
  // PRODUCT.7A2: always visible in the sidebar, same convention as
  // Supervisor — access itself is gated inside the page
  // (useAccess().can('ticket.reconcile') + PermissionState), never by
  // hiding the nav entry.
  { label: 'Reconciliação', path: '/ticket-reconciliation', icon: 'reconcile', group: 'operacao' },
  { label: 'Contatos', path: '/contacts', icon: 'contacts', group: 'operacao' },
  { label: 'Relatórios', path: '/reports', icon: 'reports', group: 'operacao', mockBacked: true },
  { label: 'Supervisor', path: '/supervisor', icon: 'supervisor', group: 'operacao' },
  // ADR-0019: always visible like Supervisor; each area is gated inside the page by flow.view / flow_template.view / flow_run.view.
  { label: 'Automação', path: '/flows', icon: 'sparkles', group: 'automacao' },
  { label: 'Canais', path: '/channels', icon: 'channels', group: 'automacao' },
  // ADR-0015: always visible, like Supervisor; access is gated inside the page (group.read).
  { label: 'Grupos', path: '/groups', icon: 'users', group: 'automacao' },
  { label: 'Pessoas', path: '/settings/people', icon: 'id-card', group: 'configuracao', alsoActiveOn: '/settings/roles' },
  { label: 'Setores e horários', path: '/settings/attendance', icon: 'clock', group: 'configuracao', alsoActiveOn: '/settings/attendance/sectors,/settings/attendance/calendars,/settings/attendance/shifts,/settings/attendance/overflow' },
  // ADR-0048: aplicações externas (CRMs) que usam a API headless. Sempre visível como as outras entradas de administração; o acesso é conferido dentro da página
  // (integration.read / integration.manage) e a própria página explica quando o recurso não está ligado neste ambiente.
  { label: 'Integrações', path: '/settings/integrations/apps', icon: 'plug', group: 'configuracao', alsoActiveOn: '/settings/integrations' },
  { label: 'Configurações', path: '/settings/general', icon: 'settings', group: 'configuracao' },
]
