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
    title: 'Grupos',
    items: [
      { key: 'group.read', label: 'Ler os grupos do WhatsApp habilitados' },
      { key: 'group.manage', label: 'Habilitar grupos e apagar o histórico' },
    ],
  },
  {
    title: 'Equipe',
    items: [
      { key: 'membership.read', label: 'Visualizar equipe' },
      { key: 'membership.manage', label: 'Gerenciar equipe' },
    ],
  },
  {
    title: 'Automação',
    items: [
      { key: 'flow.view', label: 'Ver fluxos de atendimento' },
      { key: 'flow.create', label: 'Criar fluxos' },
      { key: 'flow.edit', label: 'Editar o rascunho de um fluxo' },
      { key: 'flow.test', label: 'Simular fluxos' },
      { key: 'flow.publish', label: 'Publicar fluxos e voltar a uma versão anterior' },
      { key: 'flow.archive', label: 'Arquivar fluxos' },
      { key: 'flow_template.view', label: 'Ver modelos e packs de fluxos' },
      { key: 'flow_template.install', label: 'Instalar modelos e packs' },
      { key: 'flow_run.view', label: 'Ver execuções dos fluxos' },
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
