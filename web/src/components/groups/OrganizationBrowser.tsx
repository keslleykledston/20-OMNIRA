import { useState } from 'react'
import { useQuery } from '@tanstack/react-query'
import { Button, Icon } from '../primitives'
import { GroupList } from './GroupList'
import { groupErrorMessage, groupsAPI, type Group } from '../../lib/groups'

const UNLINKED_KEY = '__unlinked__'

interface Props {
  allItems: Group[]
  selectedId: string | null
  onSelect: (id: string) => void
}

// "Organização": navegar primeiro por Cliente do CRM (customer_accounts), depois pelos grupos desse
// cliente — em vez da lista única e plana que "Mensagens" sempre mostrou. Mesmos dados de sempre
// (account_id + additional_accounts, ADR-0059 addendum 3/8), nenhuma chamada nova além do resumo
// agregado; nenhuma contagem de Atendimento/chamado aqui, essa fatia ainda não existe para grupos.
// Reaproveita o GroupList pronto para o segundo nível — clicar um grupo seleciona-o exatamente como na
// lista "Mensagens" (o painel da direita, com o vínculo de cliente/IA/etc., já é o mesmo).
export function OrganizationBrowser({ allItems, selectedId, onSelect }: Props) {
  const [accountId, setAccountId] = useState<string | null>(null)
  const summary = useQuery({
    queryKey: ['groups-accounts-summary'],
    queryFn: () => groupsAPI.accountsSummary(),
    retry: false,
  })

  const unlinkedCount = allItems.filter((g) => !g.account_id && g.additional_accounts.length === 0).length
  const accounts = summary.data?.items ?? []

  if (!accountId) {
    return (
      <div className="flex-1 overflow-y-auto">
        {summary.isError ? (
          <p role="alert" className="p-4 text-sm text-status-danger">
            {groupErrorMessage(summary.error)}
          </p>
        ) : summary.isLoading ? (
          <div className="p-4 text-center text-sm text-text-secondary">Carregando...</div>
        ) : accounts.length === 0 && unlinkedCount === 0 ? (
          <p className="p-6 text-center text-sm text-text-secondary">Nenhum cliente do CRM cadastrado ainda.</p>
        ) : (
          <ul>
            {accounts.map((a) => (
              <li key={a.account_id} className="border-b border-border-subtle">
                <button
                  type="button"
                  onClick={() => setAccountId(a.account_id)}
                  className="flex w-full items-center justify-between gap-2 px-4 py-2.5 text-left text-sm hover:bg-surface-muted"
                >
                  <span className="truncate font-medium text-text-primary">{a.account_name}</span>
                  <span className="flex-shrink-0 text-xs text-text-tertiary">
                    {a.group_count} grupo{a.group_count === 1 ? '' : 's'}
                  </span>
                </button>
              </li>
            ))}
            {unlinkedCount > 0 && (
              <li className="border-b border-border-subtle">
                <button
                  type="button"
                  onClick={() => setAccountId(UNLINKED_KEY)}
                  className="flex w-full items-center justify-between gap-2 px-4 py-2.5 text-left text-sm hover:bg-surface-muted"
                >
                  <span className="truncate text-text-secondary">Sem cliente vinculado</span>
                  <span className="flex-shrink-0 text-xs text-text-tertiary">
                    {unlinkedCount} grupo{unlinkedCount === 1 ? '' : 's'}
                  </span>
                </button>
              </li>
            )}
          </ul>
        )}
      </div>
    )
  }

  const accountName = accountId === UNLINKED_KEY ? 'Sem cliente vinculado' : accounts.find((a) => a.account_id === accountId)?.account_name
  const groupsForAccount =
    accountId === UNLINKED_KEY
      ? allItems.filter((g) => !g.account_id && g.additional_accounts.length === 0)
      : allItems.filter((g) => g.account_id === accountId || g.additional_accounts.some((a) => a.id === accountId))

  return (
    <div className="flex h-full flex-col">
      <div className="flex items-center gap-2 border-b border-border-subtle px-3 py-2">
        <Button variant="tertiary" size="sm" onClick={() => setAccountId(null)}>
          <Icon name="arrow-left" size={14} /> Clientes
        </Button>
        <span className="truncate text-sm font-medium text-text-primary">{accountName}</span>
      </div>
      <div className="flex-1 overflow-y-auto">
        {groupsForAccount.length === 0 ? (
          <p className="p-6 text-center text-sm text-text-secondary">Nenhum grupo vinculado a este cliente ainda.</p>
        ) : (
          <GroupList groups={groupsForAccount} selectedId={selectedId} onSelect={onSelect} isLoading={false} />
        )}
      </div>
    </div>
  )
}
