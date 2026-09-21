/**
 * Exemplo: seção de auditoria que só aparece se o usuário tiver audit.read
 */
import { useAccess, useCan } from '../lib/useAccess'

export function AuditSection() {
  const access = useAccess()
  const can = useCan()

  // Renderização condicional baseada em permissão
  if (!can('audit.read')) {
    return null
  }

  return (
    <div className="audit-section">
      <h2>Logs de Auditoria</h2>
      <p>Seu papel: {access?.role_key}</p>
      <p>Permissões: {access?.permissions.join(', ')}</p>
      {/* Implementar fetch de /audit aqui */}
    </div>
  )
}

/**
 * Exemplo: componente com múltiplas actions baseadas em permissões
 */
export function TeamManagementSection() {
  const can = useCan()

  return (
    <div>
      {can('membership.read') && (
        <button>Listar Membros</button>
      )}
      {can('membership.manage') && (
        <button>Convidar Membro</button>
      )}
      {can('audit.read') && (
        <button>Ver Auditoria</button>
      )}
      {can('tenant.manage') && (
        <button>Editar Configurações do Tenant</button>
      )}
    </div>
  )
}
