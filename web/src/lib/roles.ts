// roles.name vem do banco em inglês ("Tenant Administrator"); toda tela que
// mostra papel para humano usa este rótulo em português, para não misturar
// idioma na mesma UI. Compartilhado entre Pessoas e AcceptInvitePage.
export const ROLE_DISPLAY_NAME: Record<string, string> = {
  tenant_admin: 'Administrador',
  tenant_supervisor: 'Supervisor',
  tenant_agent: 'Agente',
}

// Conferido com role_permissions: só o administrador tem membership.manage; o supervisor tem agent.manage e conversation.manage.
export const ROLE_SUMMARY: Record<string, string> = {
  tenant_admin: 'Faz tudo nesta instância: convida pessoas, muda funções e configura canais, filas, horários e automações.',
  tenant_supervisor: 'Organiza o atendimento: escolhe quem atende e em quais filas e gerencia as conversas. Não convida pessoas nem muda funções.',
  tenant_agent: 'Atende conversas: assume, responde e abre ou atualiza chamados. Não muda configurações.',
}

export function displayRoleName(roleKey: string | undefined, fallback: string): string {
  if (roleKey && ROLE_DISPLAY_NAME[roleKey]) return ROLE_DISPLAY_NAME[roleKey]
  return fallback
}
