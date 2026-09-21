/**
 * Funções e permissões — página read-only que exibe a matriz de controle de acesso.
 * IAM3 MVP: apenas visualização, sem criação de papéis customizados.
 */
import { useAccess } from '../lib/useAccess'

interface PermissionRow {
  key: string
  name: string
  description: string
}

const PERMISSION_MATRIX: PermissionRow[] = [
  { key: 'tenant.read', name: 'Ler tenant', description: 'Ver informações do tenant' },
  { key: 'tenant.manage', name: 'Gerenciar tenant', description: 'Editar configurações do tenant' },
  { key: 'membership.read', name: 'Ler membros', description: 'Listar membros da equipe' },
  { key: 'membership.manage', name: 'Gerenciar membros', description: 'Convidar/revogar acesso' },
  { key: 'audit.read', name: 'Ler auditoria', description: 'Ver logs de auditoria' },
  { key: 'conversation.read', name: 'Ver conversas', description: 'Acessar conversas' },
  { key: 'conversation.claim', name: 'Reivindicar conversa', description: 'Reivindicar e responder conversas' },
  { key: 'conversation.manage', name: 'Gerenciar conversas', description: 'Atribuir conversas a outros agentes' },
  { key: 'channel.read', name: 'Ver canais', description: 'Acessar canais WhatsApp' },
  { key: 'channel.manage', name: 'Gerenciar canais', description: 'Criar e operar conexões WhatsApp' },
]

const ROLE_DESCRIPTIONS: Record<string, string> = {
  tenant_admin: 'Controle total do tenant. Gerencia equipe, permissões, auditoria e canais.',
  tenant_supervisor: 'Supervisão de conversas. Acesso a relatórios e auditoria. Sem controle de permissões.',
  tenant_agent: 'Agente de atendimento. Acesso apenas a conversas próprias.',
}

export function RolesPermissionsPage() {
  const access = useAccess()

  return (
    <div className="roles-permissions-page p-6 max-w-6xl">
      <h1 className="text-3xl font-bold mb-2">Funções e Permissões</h1>
      <p className="text-gray-600 mb-6">
        Visualize as permissões associadas ao seu papel. Customização de papéis não está disponível no MVP.
      </p>

      {/* Seu Papel */}
      <div className="mb-8 p-4 border rounded-lg bg-blue-50">
        <h2 className="text-lg font-semibold mb-2">Seu Papel Atual</h2>
        <p className="text-xl font-bold mb-1">{access?.role_key.replace('tenant_', '').toUpperCase()}</p>
        <p className="text-gray-700">{ROLE_DESCRIPTIONS[access?.role_key || ''] || 'Papel desconhecido'}</p>
      </div>

      {/* Suas Permissões */}
      {access?.permissions && (
        <div className="mb-8">
          <h2 className="text-lg font-semibold mb-4">Suas Permissões</h2>
          <div className="grid grid-cols-1 md:grid-cols-2 gap-3">
            {PERMISSION_MATRIX.filter((p) =>
              access.permissions.includes(p.key)
            ).map((p) => (
              <div key={p.key} className="p-3 border rounded bg-green-50 border-green-200">
                <p className="font-semibold text-green-900">{p.name}</p>
                <p className="text-sm text-gray-600">{p.description}</p>
                <code className="text-xs text-gray-500 mt-1 block">{p.key}</code>
              </div>
            ))}
          </div>
        </div>
      )}

      {/* Matriz Completa (referência) */}
      <div className="mt-8">
        <h2 className="text-lg font-semibold mb-4">Matriz de Permissões Disponíveis</h2>
        <div className="overflow-x-auto">
          <table className="w-full border-collapse text-sm">
            <thead>
              <tr className="bg-gray-100">
                <th className="border p-2 text-left">Permissão</th>
                <th className="border p-2 text-left">Nome</th>
                <th className="border p-2 text-left">Descrição</th>
                <th className="border p-2 text-center">Você tem?</th>
              </tr>
            </thead>
            <tbody>
              {PERMISSION_MATRIX.map((p) => (
                <tr key={p.key} className="hover:bg-gray-50">
                  <td className="border p-2 font-mono text-xs">{p.key}</td>
                  <td className="border p-2 font-semibold">{p.name}</td>
                  <td className="border p-2">{p.description}</td>
                  <td className="border p-2 text-center">
                    {access?.permissions.includes(p.key) ? (
                      <span className="text-green-600 font-bold">✓</span>
                    ) : (
                      <span className="text-gray-400">—</span>
                    )}
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      </div>

      {/* Nota sobre convites */}
      {access?.invitation_delivery_available && (
        <div className="mt-6 p-4 border border-yellow-200 bg-yellow-50 rounded">
          <p className="text-sm text-gray-700">
            <strong>Dica:</strong> Você pode convidar novos membros para o tenant se tiver permissão <code className="bg-white px-1">membership.manage</code>.
          </p>
        </div>
      )}
    </div>
  )
}
