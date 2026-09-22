// Rótulos em português das permissões reais do catálogo (tabela permissions).
// Só entram chaves que o backend de fato checa em alguma rota; qualquer chave
// concedida a um papel e ausente daqui aparece em "Outras" com a chave crua, para
// que nada concedido fique escondido na matriz.
export interface PermissionItem {
  key: string
  label: string
}

export interface PermissionGroup {
  title: string
  items: PermissionItem[]
}

export const PERMISSION_GROUPS: PermissionGroup[] = [
  {
    title: 'Conversas',
    items: [
      { key: 'conversation.claim', label: 'Assumir e responder conversas' },
      { key: 'conversation.manage', label: 'Gerenciar e transferir conversas' },
    ],
  },
  { title: 'Canais', items: [{ key: 'channel.manage', label: 'Gerenciar canais' }] },
  {
    title: 'Equipe',
    items: [
      { key: 'membership.read', label: 'Visualizar equipe' },
      { key: 'membership.manage', label: 'Gerenciar equipe' },
    ],
  },
  { title: 'Auditoria', items: [{ key: 'audit.read', label: 'Visualizar auditoria' }] },
  {
    title: 'Organização',
    items: [
      { key: 'tenant.read', label: 'Visualizar organização' },
      { key: 'tenant.manage', label: 'Gerenciar organização' },
    ],
  },
]

const LISTED = new Set(PERMISSION_GROUPS.flatMap((g) => g.items.map((i) => i.key)))

export function unlistedPermissions(permissions: string[]): string[] {
  return permissions.filter((p) => !LISTED.has(p)).sort()
}
