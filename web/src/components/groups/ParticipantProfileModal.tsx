import { useEffect, useMemo, useRef, useState } from 'react'
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { Button, Icon, IconButton, Modal } from '../primitives'
import { useDebounced } from '../../hooks/useDebounced'
import { accountsAPI } from '../../lib/accounts'
import { accountDirectoryAPI, RELATIONSHIP_LABEL, type RelationshipType } from '../../lib/contacts'
import { groupErrorMessage, groupsAPI, type GroupAccountRef } from '../../lib/groups'
import { getTenantId } from '../../lib/session'

interface Props {
  open: boolean
  groupId: string
  participantId: string | null
  onClose: () => void
}

type Option = { key: string; label: string; detail?: string; ref: GroupAccountRef }

function sameAccountRef(a: GroupAccountRef | null, b: GroupAccountRef): boolean {
  if (!a) return false
  if ('accountId' in a && 'accountId' in b) return a.accountId === b.accountId
  if ('directoryCompanyId' in a && 'directoryCompanyId' in b) return a.directoryCompanyId === b.directoryCompanyId
  return false
}

// ADR-0015 addendum: identifica quem escreve num grupo SEM telefone (channel_participants, nunca contacts —
// author_jid é frequentemente um @lid opaco). "Nome aparente" é o mesmo conceito de contacts.alias; "empresa"
// e "função" reaproveitam exatamente o fluxo já usado para vincular um Cliente do CRM ao GRUPO (mesmo
// account_id/directory_company_id, mesmo vocabulário de contact_account_links.relationship_type).
export function ParticipantProfileModal({ open, groupId, participantId, onClose }: Props) {
  const tenantId = getTenantId()
  const queryClient = useQueryClient()
  const [alias, setAlias] = useState('')
  const [companyQuery, setCompanyQuery] = useState('')
  const debouncedCompanyQuery = useDebounced(companyQuery, 300)
  const [role, setRole] = useState<RelationshipType>('other')
  // ADR-0015 addendum 3: a empresa vinculada pode ser cliente do Cliente (ex.: Uplink, cliente da 4WNET),
  // não a própria empresa do Cliente.
  const [isDownstreamClient, setIsDownstreamClient] = useState(false)
  // A empresa escolhida para vincular/editar — só é salva ao clicar o ícone de confirmar, nunca ao clicar a
  // empresa na lista. Permite ajustar função/Cliente de um vínculo já existente sem removê-lo primeiro.
  const [pendingRef, setPendingRef] = useState<GroupAccountRef | null>(null)
  // Nenhuma ação aqui fecha o modal sozinha — só "Fechar"/X — para dar pra encadear vários ajustes (alias,
  // função, Cliente, trocar/remover empresa) na mesma sessão sem reabrir. Esta mensagem é o retorno visual.
  const [message, setMessage] = useState<{ kind: 'ok' | 'error'; text: string } | null>(null)

  const participants = useQuery({
    queryKey: ['group-participants', tenantId, groupId],
    queryFn: () => groupsAPI.participants(groupId),
    enabled: open && !!participantId,
  })
  const current = participants.data?.items.find((p) => p.id === participantId) ?? null

  // Inicializa os campos exatamente uma vez por (abertura, participante), quando os dados chegam — nunca de
  // novo a cada refetch em segundo plano, senão um clique do usuário entre o fetch disparar e resolver
  // (a query de participantes é assíncrona) seria desfeito pelo próprio efeito.
  const initializedForRef = useRef<string | null>(null)
  useEffect(() => {
    if (!open) {
      initializedForRef.current = null
      return
    }
    if (!participantId || !participants.isSuccess || initializedForRef.current === participantId) return
    initializedForRef.current = participantId
    setAlias(current?.alias ?? '')
    setRole(current?.company_role ?? 'other')
    setIsDownstreamClient(current?.is_downstream_client ?? false)
    setPendingRef(current?.company_account_id ? { accountId: current.company_account_id } : null)
    setCompanyQuery('')
    setMessage(null)
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [open, participantId, participants.isSuccess])

  const companySearch = useQuery({
    queryKey: ['group-participant-account-search', tenantId, debouncedCompanyQuery],
    queryFn: () => accountsAPI.list({ q: debouncedCompanyQuery }),
    enabled: open,
  })
  const companyDirectory = useQuery({
    queryKey: ['group-participant-account-directory', tenantId],
    queryFn: () => accountDirectoryAPI.directory(),
    enabled: open,
    retry: false,
  })
  const companyOptions = useMemo<Option[]>(() => {
    const dirItems = companyDirectory.data?.items ?? []
    const seen = new Set(dirItems.map((c) => c.name.trim().toLowerCase()))
    const needle = companyQuery.trim().toLowerCase()
    const dir = dirItems
      .filter((c) => !needle || c.name.toLowerCase().includes(needle) || (c.cnpj ?? '').includes(needle))
      .map((c): Option => ({ key: `d:${c.id}`, label: c.name, detail: c.cnpj ?? 'CRM', ref: { directoryCompanyId: c.id } }))
    const local = (companySearch.data ?? [])
      .filter((a) => !seen.has(a.name.trim().toLowerCase()))
      .map((a): Option => ({ key: `a:${a.id}`, label: a.name, detail: 'Conta local', ref: { accountId: a.id } }))
    return [...dir, ...local]
  }, [companyDirectory.data, companySearch.data, companyQuery])

  const invalidate = () => {
    void queryClient.invalidateQueries({ queryKey: ['group-participants', tenantId, groupId] })
    void queryClient.invalidateQueries({ queryKey: ['group-messages', tenantId, groupId] })
  }

  const fail = (err: unknown) => setMessage({ kind: 'error', text: groupErrorMessage(err) })

  const saveAlias = useMutation({
    mutationFn: () => groupsAPI.setParticipantAlias(participantId!, alias.trim()),
    onSuccess: () => {
      invalidate()
      setMessage({ kind: 'ok', text: 'Nome aparente salvo.' })
    },
    onError: fail,
  })
  const saveCompany = useMutation({
    mutationFn: (ref: GroupAccountRef) => groupsAPI.setParticipantCompany(participantId!, ref, role, isDownstreamClient),
    onSuccess: () => {
      invalidate()
      setMessage({ kind: 'ok', text: 'Empresa salva.' })
    },
    onError: fail,
  })
  const removeCompany = useMutation({
    mutationFn: () => groupsAPI.unlinkParticipantCompany(participantId!),
    onSuccess: () => {
      setPendingRef(null)
      invalidate()
      setMessage({ kind: 'ok', text: 'Vínculo removido.' })
    },
    onError: fail,
  })

  const pending = saveAlias.isPending || saveCompany.isPending || removeCompany.isPending

  return (
    <Modal
      open={open}
      title="Identificar participante"
      description={`"${current?.display_name || 'Quem escreveu'}" — nome aparente e empresa valem em todos os grupos desta conexão.`}
      onClose={onClose}
      footer={
        <Button variant="secondary" size="sm" onClick={onClose} disabled={pending}>
          Fechar
        </Button>
      }
    >
      <div className="space-y-4">
        <div className="space-y-1.5">
          <label htmlFor="participant-alias" className="text-xs font-medium text-text-secondary">
            Nome aparente
          </label>
          <div className="flex gap-2">
            <input
              id="participant-alias"
              type="text"
              value={alias}
              onChange={(e) => setAlias(e.target.value)}
              placeholder={current?.display_name || 'Nome no WhatsApp'}
              maxLength={200}
              className="h-9 w-full rounded-control border border-border-light bg-surface px-2 text-sm text-text-primary focus-visible:ring-2 focus-visible:ring-accent-primary"
            />
            <IconButton
              type="button"
              size="sm"
              variant="primary"
              disabled={pending}
              onClick={() => saveAlias.mutate()}
              title="Salvar nome aparente"
              aria-label="Salvar nome aparente"
            >
              <Icon name="check" size={16} />
            </IconButton>
          </div>
        </div>

        <div className="space-y-1.5 border-t border-border-subtle pt-3">
          <p className="text-xs font-medium text-text-secondary">Empresa</p>
          {current?.company_name ? (
            <div className="flex items-center justify-between gap-2 rounded-control bg-surface-muted px-2 py-1 text-xs">
              <span className="truncate text-text-primary">
                {current.company_name}
                {current.company_role && <span className="ml-1 text-text-tertiary">· {RELATIONSHIP_LABEL[current.company_role]}</span>}
                {current.is_downstream_client && <span className="ml-1 text-text-tertiary">· Cliente</span>}
              </span>
              <span className="flex flex-shrink-0 items-center gap-1">
                <IconButton
                  size="sm"
                  variant="primary"
                  disabled={pending || !current.company_account_id}
                  onClick={() => current.company_account_id && saveCompany.mutate({ accountId: current.company_account_id })}
                  title="Editar função/Cliente desta empresa"
                  aria-label="Editar função/Cliente desta empresa"
                >
                  <Icon name="edit" size={12} />
                </IconButton>
                <IconButton
                  size="sm"
                  variant="danger"
                  disabled={pending}
                  onClick={() => removeCompany.mutate()}
                  title="Remover vínculo"
                  aria-label="Remover vínculo"
                >
                  <Icon name="trash" size={12} />
                </IconButton>
              </span>
            </div>
          ) : (
            <p className="text-xs text-text-tertiary">Nenhuma empresa vinculada.</p>
          )}
          <input
            type="text"
            value={companyQuery}
            onChange={(e) => setCompanyQuery(e.target.value)}
            placeholder="Buscar empresa..."
            aria-label="Buscar empresa"
            className="h-9 w-full rounded-control border border-border-light bg-surface px-2 text-sm text-text-primary focus-visible:ring-2 focus-visible:ring-accent-primary"
          />
          <label className="flex items-center gap-2 text-xs text-text-secondary">
            Função
            <select
              aria-label="Função"
              value={role}
              onChange={(e) => setRole(e.target.value as RelationshipType)}
              className="flex-1 rounded-control border border-border-subtle bg-surface px-2 py-1 text-xs text-text-primary focus:outline-none focus-visible:ring-2 focus-visible:ring-accent-primary"
            >
              {(Object.keys(RELATIONSHIP_LABEL) as RelationshipType[]).map((r) => (
                <option key={r} value={r}>
                  {RELATIONSHIP_LABEL[r]}
                </option>
              ))}
            </select>
          </label>
          <label className="flex items-start gap-2 text-xs text-text-secondary">
            <input
              type="checkbox"
              className="mt-0.5"
              checked={isDownstreamClient}
              onChange={(e) => setIsDownstreamClient(e.target.checked)}
            />
            <span>
              Cliente
              <span className="ml-1 text-text-tertiary">— marque quando a empresa acima é cliente do Cliente (ex.: a Uplink, cliente da 4WNET).</span>
            </span>
          </label>
          {companyDirectory.isError && <p className="text-xs text-status-warning">Diretório de empresas do CRM indisponível: só contas já existentes aparecem.</p>}
          <ul className="max-h-40 overflow-y-auto rounded-control border border-border-subtle bg-surface" role="listbox" aria-label="Empresas">
            {companySearch.isLoading || companyDirectory.isLoading ? (
              <li className="px-2 py-1.5 text-xs text-text-tertiary">Carregando...</li>
            ) : companyOptions.length === 0 ? (
              <li className="px-2 py-1.5 text-xs text-text-tertiary">Nenhuma empresa encontrada.</li>
            ) : (
              companyOptions.map((o) => {
                const selected = sameAccountRef(pendingRef, o.ref)
                return (
                  <li key={o.key} role="option" aria-selected={selected}>
                    <button
                      type="button"
                      disabled={pending}
                      onClick={() => setPendingRef(o.ref)}
                      className={`flex w-full items-center justify-between gap-2 truncate px-2 py-1.5 text-left text-sm hover:bg-surface-muted ${selected ? 'bg-accent-primary-soft text-accent-primary' : 'text-text-primary'}`}
                    >
                      <span className="flex min-w-0 items-center gap-1.5 truncate">
                        {selected && <Icon name="check" size={12} className="shrink-0" />}
                        <span className="truncate">{o.label}</span>
                      </span>
                      {o.detail && <span className="shrink-0 text-xs text-text-tertiary">{o.detail}</span>}
                    </button>
                  </li>
                )
              })
            )}
          </ul>
          <div className="flex justify-end">
            <IconButton
              type="button"
              size="sm"
              variant="primary"
              disabled={pending || !pendingRef}
              onClick={() => pendingRef && saveCompany.mutate(pendingRef)}
              title="Vincular empresa"
              aria-label="Vincular empresa"
            >
              <Icon name="plus" size={16} />
            </IconButton>
          </div>
        </div>

        {message && (
          <p role={message.kind === 'error' ? 'alert' : 'status'} className={message.kind === 'error' ? 'text-sm text-status-danger' : 'text-sm text-status-success'}>
            {message.text}
          </p>
        )}
      </div>
    </Modal>
  )
}
