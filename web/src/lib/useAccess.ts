import { useCallback } from 'react'
import { useQuery } from '@tanstack/react-query'
import { getTenantId } from './session'
import { isActing } from './acting'
import { teamAPI } from './team'

// Único ponto de autorização no frontend: as permissões efetivas vêm do backend
// (GET /me/access) e a UI só pergunta `can('chave')`. Nunca comparar o nome do
// papel (role_key) para decidir o que mostrar. O backend segue sendo a autoridade
// em toda requisição; isto só evita oferecer uma ação que ele vai recusar.
export function useAccess() {
  const tenantId = getTenantId()
  // Attending through the Hub the keys can be withdrawn at any moment: ask again often (the server refuses the operation either way; this only keeps
  // the screen from offering what it will refuse).
  const acting = isActing()
  const query = useQuery({
    queryKey: ['me-access', tenantId],
    queryFn: () => teamAPI.myAccess(),
    enabled: !!tenantId,
    retry: false,
    staleTime: acting ? 0 : 30_000,
    refetchInterval: acting ? 15_000 : false,
  })
  const permissions = query.data?.permissions
  const can = useCallback((permission: string) => permissions?.includes(permission) ?? false, [permissions])
  return { data: query.data, isLoading: query.isLoading, isError: query.isError, can }
}
