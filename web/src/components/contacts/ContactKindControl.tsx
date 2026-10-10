import { useCallback, useEffect, useMemo, useState } from 'react'
import clsx from 'clsx'
import { Button, ConfirmDialog } from '../primitives'
import {
  CONTACT_KIND_LABEL,
  INTERNAL_ROLES,
  INTERNAL_ROLE_LABEL,
  RELATIONSHIP_LABEL,
  accountDirectoryAPI,
  classificationAPI,
  contactKindErrorMessage,
  type AccountRef,
  type CompanySuggestion,
  type ContactAccountLink,
  type ContactKind,
  type CustomerAccount,
  type DirectoryCompany,
  type InternalRole,
  type RelationshipType,
} from '../../lib/contacts'

interface Props {
  contactId: string
  kind: ContactKind
  /** The role of an internal contact; when absent it is read from the classification endpoint. */
  internalRole?: InternalRole | null
  contactName?: string
  /** A Hub agent attending the instance (ADR-0040): no "Interno" (the instance's own call), and companies only from the accounts that already exist (the ERP directory is phase 04b). */
  delegated?: boolean
  /** Called after the API accepted the change, so the parent can refresh what depends on it. */
  onChanged: (kind: ContactKind) => void
}

type Option = { key: string; label: string; detail?: string; ref: AccountRef }

// What "Interno" changes, said every time it is chosen: a customer marked internal by mistake would silently lose the bot,
// the automatic ticket and the SLA, so the person deciding must know.
const INTERNAL_NOTICE =
  'Conversas internas não recebem bot, chamado automático, SLA nem pesquisa de satisfação, mas continuam na fila e podem ser atribuídas.'

const FIELD =
  'w-full rounded-control border border-border-subtle bg-surface px-2 py-1.5 text-xs text-text-primary ' +
  'focus:outline-none focus-visible:ring-2 focus-visible:ring-accent-primary'

// Lets whoever attends say who this contact is (ADR-0018). "Cliente" needs at least one company: it is chosen from the
// tenant's company directory (the server revalidates it) or from existing accounts, and the contact may belong to several.
// Spam asks first; it is never deleted, it has its own Inbox, and "Não é spam" there undoes a false positive.
export function ContactKindControl({ contactId, kind, internalRole, contactName, delegated = false, onChanged }: Props) {
  const [pending, setPending] = useState(false)
  const [error, setError] = useState<string | null>(null)
  const [confirmSpam, setConfirmSpam] = useState(false)
  const [links, setLinks] = useState<ContactAccountLink[]>([])
  // 'become': choose the company that makes this contact a customer; 'add': link one more company to a customer or to an "other" contact
  const [picking, setPicking] = useState<'become' | 'add' | null>(null)
  const [removing, setRemoving] = useState<ContactAccountLink | null>(null)
  // "Interno" needs its role (team, partner, supplier): chosen in a small panel before anything is saved
  const [pickingInternal, setPickingInternal] = useState(false)
  const [role, setRole] = useState<InternalRole | null>(internalRole ?? null)

  const loadLinks = useCallback(async () => {
    try {
      const c = await classificationAPI.get(contactId)
      setLinks(c.accounts ?? [])
      setRole(c.kind === 'internal' ? (c.internal_role ?? null) : null)
    } catch {
      setLinks([])
    }
  }, [contactId])

  useEffect(() => {
    if (kind === 'customer' || kind === 'other' || kind === 'internal') void loadLinks()
    else setLinks([])
    if (kind !== 'internal') setRole(null)
    setPicking(null)
    setPickingInternal(false)
  }, [kind, contactId, loadLinks])

  useEffect(() => {
    if (internalRole) setRole(internalRole)
  }, [internalRole])

  const run = async (action: () => Promise<unknown>, next?: ContactKind) => {
    if (pending) return
    setPending(true)
    setError(null)
    try {
      await action()
      setConfirmSpam(false)
      setPicking(null)
      setPickingInternal(false)
      setRemoving(null)
      if (next) onChanged(next)
      else {
        await loadLinks()
        onChanged(kind)
      }
    } catch (err) {
      setError(contactKindErrorMessage(err))
      setConfirmSpam(false)
    } finally {
      setPending(false)
    }
  }

  const change = (next: ContactKind) => {
    if (next === kind) return
    if (next === 'customer') {
      setPicking('become')
      return
    }
    if (next === 'internal') {
      setPickingInternal(true)
      return
    }
    void run(() => classificationAPI.put(contactId, { kind: next }), next)
  }

  const becomeCustomer = (ref: AccountRef) =>
    run(() => classificationAPI.put(contactId, { kind: 'customer', accounts: [ref] }), 'customer')
  // Same kind, other role (or first time internal): one request, the server audits who decided and when.
  const saveInternal = (next: InternalRole) =>
    run(async () => {
      await classificationAPI.put(contactId, { kind: 'internal', internal_role: next })
      setRole(next)
    }, 'internal')
  const addCompany = (ref: AccountRef) => run(() => classificationAPI.linkAccount(contactId, ref))

  const remove = (link: ContactAccountLink, reclassifyTo?: 'other' | 'unclassified') =>
    run(() => classificationAPI.endLink(contactId, link.id, reclassifyTo), reclassifyTo)

  return (
    <div className="p-4 border-b border-border-subtle" role="group" aria-label="Tipo de contato">
      <h4 className="text-xs font-semibold text-text-tertiary mb-3 uppercase">Tipo de contato</h4>

      {kind === 'spam' ? (
        <div className="rounded-control border border-status-danger-border bg-status-danger-soft p-3 text-xs text-status-danger">
          <p className="font-semibold">Marcado como spam</p>
          <p className="mt-1">
            As conversas deste contato ficam só na caixa Spam e não entram na fila. Se foi engano, restaure.
          </p>
          <div className="mt-3 flex gap-2">
            <Button size="sm" variant="secondary" onClick={() => change('other')} disabled={pending}>
              Não é spam
            </Button>
            <Button size="sm" variant="tertiary" onClick={() => change('customer')} disabled={pending}>
              É cliente
            </Button>
          </div>
        </div>
      ) : (
        <>
          <div className="flex gap-1.5">
            {(delegated ? (['customer', 'other'] as const) : (['customer', 'internal', 'other'] as const)).map((k) => (
              <button
                key={k}
                type="button"
                aria-pressed={kind === k}
                disabled={pending}
                onClick={() => change(k)}
                className={clsx(
                  'px-3 py-1 text-xs font-medium rounded-pill transition-colors disabled:opacity-60',
                  kind === k ? 'bg-accent-primary text-white' : 'bg-surface-muted text-text-secondary hover:bg-surface-tertiary',
                )}
              >
                {CONTACT_KIND_LABEL[k]}
              </button>
            ))}
          </div>
          {kind === 'unclassified' && (
            <p className="mt-2 text-xs text-text-tertiary">
              Ainda não classificado: o atendimento é limitado até você dizer se é cliente, interno ou outro contato.
            </p>
          )}
          {kind === 'internal' && delegated && (
            <p className="mt-2 text-xs text-text-tertiary">Contato interno: quem define isso é a própria empresa.</p>
          )}
          {kind === 'internal' && !delegated && (
            <div className="mt-3" role="group" aria-label="Tipo de contato interno">
              <div className="flex gap-1.5">
                {INTERNAL_ROLES.map((r) => (
                  <button
                    key={r}
                    type="button"
                    aria-pressed={role === r}
                    disabled={pending}
                    onClick={() => role !== r && void saveInternal(r)}
                    className={clsx(
                      'px-2.5 py-0.5 text-xs font-medium rounded-pill border transition-colors disabled:opacity-60',
                      role === r ? 'border-accent-primary bg-accent-primary-soft text-accent-primary' : 'border-border-subtle text-text-secondary hover:bg-surface-muted',
                    )}
                  >
                    {INTERNAL_ROLE_LABEL[r]}
                  </button>
                ))}
              </div>
              <p className="mt-2 text-xs text-text-tertiary">{INTERNAL_NOTICE}</p>
            </div>
          )}
          <button
            type="button"
            disabled={pending}
            onClick={() => setConfirmSpam(true)}
            className="mt-3 text-xs font-medium text-status-danger hover:underline disabled:opacity-60"
          >
            Marcar como spam ou golpe
          </button>
        </>
      )}

      {(kind === 'customer' || kind === 'other' || kind === 'internal') && (
        <CompanyList
          links={links}
          pending={pending}
          onAdd={() => setPicking('add')}
          onPrimary={(l) => void run(() => classificationAPI.setPrimary(contactId, l.id))}
          onRemove={(l) => setRemoving(l)}
        />
      )}

      {pickingInternal && (
        <InternalPicker
          pending={pending}
          initial={role}
          contactName={contactName}
          onCancel={() => setPickingInternal(false)}
          onConfirm={(r) => void saveInternal(r)}
        />
      )}

      {picking && (
        <CompanyPicker
          contactId={contactId}
          delegated={delegated}
          title={picking === 'add' ? 'Adicionar empresa' : 'Empresa do cliente'}
          hasCompanies={links.length > 0}
          pending={pending}
          onCancel={() => setPicking(null)}
          onConfirm={(ref) => void (picking === 'add' ? addCompany(ref) : becomeCustomer(ref))}
        />
      )}

      {error && (
        <p role="alert" className="mt-2 text-xs text-status-danger">
          {error}
        </p>
      )}

      <ConfirmDialog
        open={confirmSpam}
        title="Marcar como spam?"
        message={`${contactName ? contactName + ' deixa' : 'Este contato deixa'} a fila e a lista de conversas e passa para a caixa Spam. Nada é apagado e você pode restaurar depois.`}
        confirmLabel="Marcar como spam"
        destructive
        isPending={pending}
        onConfirm={() => change('spam')}
        onCancel={() => setConfirmSpam(false)}
      />

      {removing && (kind !== 'customer' || links.filter((l) => l.status === 'active').length > 1) && (
        <ConfirmDialog
          open
          title="Remover empresa?"
          message={`${contactName ?? 'O contato'} deixa de estar vinculado a ${removing.account_name}. O histórico é mantido.`}
          confirmLabel="Remover"
          destructive
          isPending={pending}
          onConfirm={() => void remove(removing)}
          onCancel={() => setRemoving(null)}
        />
      )}
      {removing && kind === 'customer' && links.filter((l) => l.status === 'active').length <= 1 && (
        <div role="dialog" aria-label="Última empresa" className="mt-3 rounded-control border border-border-subtle bg-surface-muted p-3 text-xs">
          <p className="font-semibold text-text-primary">Esta é a última empresa de {contactName ?? 'este contato'}.</p>
          <p className="mt-1 text-text-secondary">Um cliente precisa de uma empresa. Ao remover, reclassifique o contato:</p>
          <div className="mt-3 flex flex-wrap gap-2">
            <Button size="sm" variant="secondary" disabled={pending} onClick={() => void remove(removing, 'other')}>
              Remover e classificar como Outros
            </Button>
            <Button size="sm" variant="secondary" disabled={pending} onClick={() => void remove(removing, 'unclassified')}>
              Remover e deixar não classificado
            </Button>
            <Button size="sm" variant="tertiary" disabled={pending} onClick={() => setRemoving(null)}>
              Cancelar
            </Button>
          </div>
        </div>
      )}
    </div>
  )
}

function CompanyList({
  links,
  pending,
  onAdd,
  onPrimary,
  onRemove,
}: {
  links: ContactAccountLink[]
  pending: boolean
  onAdd: () => void
  onPrimary: (l: ContactAccountLink) => void
  onRemove: (l: ContactAccountLink) => void
}) {
  return (
    <div className="mt-3" aria-label="Empresas do cliente">
      <p className="text-xs font-semibold text-text-tertiary uppercase mb-2">Empresas</p>
      <ul className="space-y-1.5">
        {links.map((l) => (
          <li key={l.id} className="flex items-center gap-2 text-xs">
            <span className="flex-1 min-w-0">
              <span className="font-medium text-text-primary break-words">{l.account_name}</span>{' '}
              <span className="text-text-tertiary">· {RELATIONSHIP_LABEL[l.relationship_type] ?? l.relationship_type}</span>
              {l.primary && <span className="ml-1 rounded-pill bg-accent-primary/10 px-1.5 py-0.5 text-accent-primary">Principal</span>}
            </span>
            {!l.primary && links.length > 1 && (
              <button type="button" disabled={pending} onClick={() => onPrimary(l)} className="text-accent-primary hover:underline disabled:opacity-60">
                Tornar principal
              </button>
            )}
            <button
              type="button"
              disabled={pending}
              aria-label={`Remover ${l.account_name}`}
              onClick={() => onRemove(l)}
              className="text-status-danger hover:underline disabled:opacity-60"
            >
              Remover
            </button>
          </li>
        ))}
      </ul>
      <button type="button" disabled={pending} onClick={onAdd} className="mt-2 text-xs font-medium text-accent-primary hover:underline disabled:opacity-60">
        + Adicionar empresa
      </button>
    </div>
  )
}

// Picks ONE company. Directory companies are sent by id only: the server re-reads name, CNPJ and status itself.
function CompanyPicker({
  contactId,
  delegated,
  title,
  hasCompanies,
  pending,
  onCancel,
  onConfirm,
}: {
  contactId: string
  delegated: boolean
  title: string
  hasCompanies: boolean
  pending: boolean
  onCancel: () => void
  onConfirm: (ref: AccountRef) => void
}) {
  const [directory, setDirectory] = useState<DirectoryCompany[]>([])
  const [accounts, setAccounts] = useState<CustomerAccount[]>([])
  const [loading, setLoading] = useState(true)
  const [directoryDown, setDirectoryDown] = useState(false)
  const [suggestions, setSuggestions] = useState<CompanySuggestion[]>([])
  const [q, setQ] = useState('')
  const [chosen, setChosen] = useState<string>('')
  const [relationship, setRelationship] = useState<RelationshipType>('employee')
  const [primary, setPrimary] = useState(!hasCompanies)

  useEffect(() => {
    let alive = true
    void (async () => {
      // through the Hub only the accounts that already exist: the company directory is the instance's ERP (phase 04b)
      const [d, a, s] = await Promise.allSettled([
        delegated ? Promise.reject(new Error('delegated')) : accountDirectoryAPI.directory(),
        accountDirectoryAPI.localAccounts(),
        delegated ? Promise.reject(new Error('delegated')) : classificationAPI.suggestions(contactId),
      ])
      if (!alive) return
      if (s.status === 'fulfilled') setSuggestions((s.value.items ?? []).filter((x) => !x.already_linked))
      if (d.status === 'fulfilled') setDirectory(d.value.items ?? [])
      else setDirectoryDown(!delegated)
      if (a.status === 'fulfilled') setAccounts(a.value.items ?? [])
      setLoading(false)
    })()
    return () => {
      alive = false
    }
  }, [contactId, delegated])

  const options = useMemo<Option[]>(() => {
    const seen = new Set(directory.map((c) => c.name.trim().toLowerCase()))
    // Suggestions first: companies a validated ticket selection already tied to this contact. The id is sent with the
    // evidence id; the name shown comes from the directory/account, never invented.
    const sug = suggestions.map((x) => {
      const name = directory.find((c) => c.id === x.external_company_id)?.name ?? x.account_name ?? `Empresa ${x.external_company_id}`
      return {
        key: `s:${x.evidence_id}`,
        label: name,
        detail: 'Sugerida por chamado anterior',
        ref: { directory_company_id: x.external_company_id, evidence_id: x.evidence_id } as AccountRef,
      }
    })
    const suggested = new Set(suggestions.map((x) => x.external_company_id))
    const dir = directory
      .filter((c) => !suggested.has(c.id))
      .map((c) => ({ key: `d:${c.id}`, label: c.name, detail: c.cnpj, ref: { directory_company_id: c.id } }))
    const local = accounts
      .filter((a) => !seen.has(a.name.trim().toLowerCase()))
      .map((a) => ({ key: `a:${a.id}`, label: a.name, detail: 'Conta local', ref: { account_id: a.id } }))
    const needle = q.trim().toLowerCase()
    return [...sug, ...dir, ...local].filter((o) => !needle || o.label.toLowerCase().includes(needle) || (o.detail ?? '').includes(needle))
  }, [directory, accounts, suggestions, q])

  const picked = options.find((o) => o.key === chosen)

  return (
    <div role="group" aria-label={title} className="mt-3 rounded-control border border-border-subtle bg-surface-muted p-3 space-y-2">
      <p className="text-xs font-semibold text-text-primary">{title}</p>
      <input aria-label="Buscar empresa" className={FIELD} placeholder="Buscar empresa…" value={q} onChange={(e) => setQ(e.target.value)} />
      {loading ? (
        <p className="text-xs text-text-tertiary">Carregando empresas…</p>
      ) : (
        <>
          {directoryDown && <p className="text-xs text-status-warning">Diretório de empresas indisponível: só contas já existentes aparecem.</p>}
          {delegated && <p className="text-xs text-text-tertiary">Pelo Hub, só as empresas já cadastradas na instância aparecem.</p>}
          <ul className="max-h-40 overflow-auto rounded-control border border-border-subtle bg-surface" role="listbox" aria-label="Empresas">
            {options.length === 0 && <li className="px-2 py-2 text-xs text-text-tertiary">Nenhuma empresa encontrada.</li>}
            {options.map((o) => (
              <li key={o.key} role="option" aria-selected={chosen === o.key}>
                <button
                  type="button"
                  onClick={() => setChosen(o.key)}
                  className={clsx('w-full text-left px-2 py-1.5 text-xs', chosen === o.key ? 'bg-accent-primary/10 text-accent-primary' : 'hover:bg-surface-tertiary')}
                >
                  <span className="font-medium">{o.label}</span>
                  {o.detail && <span className="ml-2 text-text-tertiary">{o.detail}</span>}
                </button>
              </li>
            ))}
          </ul>
        </>
      )}
      <label className="block text-xs text-text-secondary">
        Vínculo
        <select aria-label="Vínculo" className={clsx(FIELD, 'mt-1')} value={relationship} onChange={(e) => setRelationship(e.target.value as RelationshipType)}>
          {(Object.keys(RELATIONSHIP_LABEL) as RelationshipType[]).map((r) => (
            <option key={r} value={r}>
              {RELATIONSHIP_LABEL[r]}
            </option>
          ))}
        </select>
      </label>
      {hasCompanies && (
        <label className="flex items-center gap-2 text-xs text-text-secondary">
          <input type="checkbox" checked={primary} onChange={(e) => setPrimary(e.target.checked)} />
          Tornar empresa principal
        </label>
      )}
      <div className="flex gap-2 pt-1">
        <Button size="sm" disabled={!picked || pending} onClick={() => picked && onConfirm({ ...picked.ref, relationship_type: relationship, primary })}>
          Confirmar
        </Button>
        <Button size="sm" variant="tertiary" disabled={pending} onClick={onCancel}>
          Cancelar
        </Button>
      </div>
    </div>
  )
}


// Chooses WHAT KIND of internal contact this is, and says what the choice switches off, before anything is saved.
function InternalPicker({
  pending,
  initial,
  contactName,
  onCancel,
  onConfirm,
}: {
  pending: boolean
  initial: InternalRole | null
  contactName?: string
  onCancel: () => void
  onConfirm: (role: InternalRole) => void
}) {
  const [chosen, setChosen] = useState<InternalRole | null>(initial)
  return (
    <div role="group" aria-label="Contato interno" className="mt-3 rounded-control border border-border-subtle bg-surface-muted p-3 space-y-2">
      <p className="text-xs font-semibold text-text-primary">{contactName ? `${contactName} é…` : 'Este contato é…'}</p>
      <div className="space-y-1">
        {INTERNAL_ROLES.map((r) => (
          <label key={r} className="flex items-center gap-2 text-xs text-text-primary">
            <input type="radio" name="internal-role" checked={chosen === r} onChange={() => setChosen(r)} />
            {INTERNAL_ROLE_LABEL[r]}
          </label>
        ))}
      </div>
      <p className="text-xs text-text-secondary">{INTERNAL_NOTICE}</p>
      <div className="flex gap-2 pt-1">
        <Button size="sm" disabled={!chosen || pending} onClick={() => chosen && onConfirm(chosen)}>
          Marcar como interno
        </Button>
        <Button size="sm" variant="tertiary" disabled={pending} onClick={onCancel}>
          Cancelar
        </Button>
      </div>
    </div>
  )
}
