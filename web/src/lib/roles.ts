// roles.name vem do banco em inglês ("Tenant Administrator"); toda tela que
// mostra papel para humano usa este rótulo em português, para não misturar
// idioma na mesma UI. Compartilhado entre TeamPage e AcceptInvitePage.
export const ROLE_DISPLAY_NAME: Record<string, string> = {
  tenant_admin: 'Administrador',
  tenant_supervisor: 'Supervisor',
  tenant_agent: 'Agente',
}

export function displayRoleName(roleKey: string | undefined, fallback: string): string {
  if (roleKey && ROLE_DISPLAY_NAME[roleKey]) return ROLE_DISPLAY_NAME[roleKey]
  return fallback
}
