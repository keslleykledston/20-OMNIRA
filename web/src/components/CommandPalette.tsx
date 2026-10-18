import { useEffect, useId, useMemo, useRef, useState } from 'react'
import { useNavigate } from 'react-router-dom'
import { useQuery } from '@tanstack/react-query'
import axios from 'axios'
import clsx from 'clsx'
import { Icon } from './primitives'
import type { IconName } from './primitives/Icon'
import { isDevSurface } from './UnavailableSurface'
import { navItems } from '../lib/navItems'
import { contactsAPI } from '../lib/contacts'
import { API_BASE } from '../lib/config'
import { authHeaders, getTenantId, handleUnauthorized, isUnauthorized } from '../lib/session'
import { isActing } from '../lib/acting'
import { useDebounced } from '../hooks/useDebounced'
import { MIN_REMOTE_QUERY, matchesQuery, moveActive } from '../lib/commandPalette'
import type { ConversationItem } from '../types/api'

const REMOTE_LIMIT = 5

interface Row {
  key: string
  group: string
  label: string
  hint?: string
  icon: IconName
  /** Router path, or a full address for the console (another entry of the build). */
  to: string
  external?: boolean
}

// Conversations the person can see, matched by contact name or phone on the server (the same `q` the list uses).
async function searchConversations(tenantId: string, q: string): Promise<ConversationItem[]> {
  try {
    const res = await axios.get(`${API_BASE}/tenants/${tenantId}/inbox/conversations`, { params: { q, limit: REMOTE_LIMIT }, headers: authHeaders() })
    return (res.data as { items?: ConversationItem[] }).items ?? []
  } catch (err) {
    if (isUnauthorized(err)) handleUnauthorized()
    throw err
  }
}

interface Props {
  open: boolean
  onClose: () => void
}

/** Jump to a screen, a contact or a conversation from the keyboard. Display and navigation only: every destination checks access again. */
export default function CommandPalette({ open, onClose }: Props) {
  const navigate = useNavigate()
  const listId = useId()
  const inputRef = useRef<HTMLInputElement>(null)
  const restoreTo = useRef<HTMLElement | null>(null)
  const [query, setQuery] = useState('')
  const [active, setActive] = useState(0)
  const debounced = useDebounced(query.trim(), 250)
  const tenantId = getTenantId()
  // Attending through the Hub only the conversations exist (ADR-0040): no other destination and no company-wide search.
  const acting = isActing()
  const remote = open && !acting && !!tenantId && debounced.length >= MIN_REMOTE_QUERY

  const contacts = useQuery({
    queryKey: ['palette-contacts', tenantId, debounced],
    queryFn: () => contactsAPI.list(undefined, REMOTE_LIMIT, { q: debounced }),
    enabled: remote,
    staleTime: 15_000,
  })
  const conversations = useQuery({
    queryKey: ['palette-conversations', tenantId, debounced],
    queryFn: () => searchConversations(tenantId as string, debounced),
    enabled: remote,
    staleTime: 15_000,
  })

  const rows = useMemo<Row[]>(() => {
    const screens = navItems
      .filter((i) => (!i.mockBacked || isDevSurface()) && (!acting || i.path === '/inbox'))
      .filter((i) => matchesQuery(i.label, query))
      .map<Row>((i) => ({ key: `nav:${i.path}`, group: 'Ir para', label: i.label, icon: i.icon, to: i.path }))
    // Server results belong to the debounced text: while the person is still typing something else they are stale and not offered (Enter must not open them).
    const fresh = debounced === query.trim()
    const people = (fresh ? contacts.data?.items ?? [] : []).map<Row>((c) => ({
      key: `contact:${c.id}`,
      group: 'Contatos',
      label: c.display_name || c.phone_e164 || 'Sem nome',
      hint: c.phone_e164 || undefined,
      icon: 'contacts',
      to: `/contacts/${c.id}`,
    }))
    const chats = (fresh ? conversations.data ?? [] : []).map<Row>((c) => ({
      key: `conversation:${c.id}`,
      group: 'Conversas',
      label: c.contact_name || c.contact_phone || 'Sem nome',
      hint: c.contact_phone || undefined,
      icon: 'conversations',
      to: `/inbox?conversation_id=${c.id}`,
    }))
    return [...screens, ...people, ...chats]
  }, [query, debounced, acting, contacts.data, conversations.data])

  // A fresh palette every time it opens; the focus goes back to where it was when it closes.
  useEffect(() => {
    if (!open) return
    restoreTo.current = document.activeElement as HTMLElement | null
    setQuery('')
    setActive(0)
    inputRef.current?.focus()
    return () => restoreTo.current?.focus()
  }, [open])

  // A new result list starts at the top.
  useEffect(() => setActive(0), [query, debounced, contacts.data, conversations.data])

  if (!open) return null

  const go = (row: Row) => {
    onClose()
    if (row.external) window.location.assign(row.to)
    else navigate(row.to)
  }

  const onKeyDown = (e: React.KeyboardEvent) => {
    if (e.key === 'Escape') {
      e.preventDefault()
      onClose()
    } else if (e.key === 'ArrowDown') {
      e.preventDefault()
      setActive((a) => moveActive(a, 1, rows.length))
    } else if (e.key === 'ArrowUp') {
      e.preventDefault()
      setActive((a) => moveActive(a, -1, rows.length))
    } else if (e.key === 'Enter') {
      e.preventDefault()
      const row = rows[active]
      if (row) go(row)
    } else if (e.key === 'Tab') {
      // the palette is one control: Tab does not wander off behind the overlay
      e.preventDefault()
    }
  }

  const typed = query.trim()
  const searching = !acting && typed.length >= MIN_REMOTE_QUERY && (debounced !== typed || contacts.isFetching || conversations.isFetching)
  const failed = remote && (contacts.isError || conversations.isError)
  // rows keep their flat position (the keyboard walks the flat list); the groups are only how they are shown
  const groups: { name: string; rows: { row: Row; index: number }[] }[] = []
  rows.forEach((row, index) => {
    const last = groups[groups.length - 1]
    if (last && last.name === row.group) last.rows.push({ row, index })
    else groups.push({ name: row.group, rows: [{ row, index }] })
  })

  return (
    <div
      className="fixed inset-0 z-50 flex items-start justify-center p-4 pt-[12vh]"
      style={{ backgroundColor: 'var(--color-surface-overlay)', margin: 0 }}
      onMouseDown={(e) => {
        if (e.target === e.currentTarget) onClose()
      }}
    >
      <div role="dialog" aria-modal="true" aria-label="Buscar" onKeyDown={onKeyDown} className="flex max-h-[70dvh] w-full max-w-xl flex-col overflow-hidden rounded-sheet bg-surface shadow-lg">
        <div className="flex flex-shrink-0 items-center gap-3 border-b border-border-subtle px-4 py-3">
          <Icon name="search" size={18} className="text-text-tertiary" />
          <input
            ref={inputRef}
            role="combobox"
            aria-expanded="true"
            aria-controls={listId}
            aria-activedescendant={rows[active] ? `${listId}-${active}` : undefined}
            aria-label="Buscar telas, contatos e conversas"
            autoComplete="off"
            value={query}
            onChange={(e) => setQuery(e.target.value)}
            placeholder={acting ? 'Buscar…' : 'Buscar tela, contato, telefone ou conversa…'}
            className="min-w-0 flex-1 bg-transparent text-body text-text-primary outline-none placeholder:text-text-tertiary"
          />
          <kbd className="hidden rounded-control border border-border-subtle px-1.5 py-0.5 text-[11px] text-text-tertiary sm:inline">Esc</kbd>
        </div>
        <div id={listId} role="listbox" aria-label="Resultados" className="min-h-0 flex-1 overflow-y-auto py-2">
          {groups.map((g) => (
            <div key={g.name} role="group" aria-label={g.name}>
              <p aria-hidden="true" className="px-4 pb-1 pt-2 text-[11px] font-semibold uppercase tracking-wider text-text-tertiary">{g.name}</p>
              {g.rows.map(({ row, index: i }) => (
                <div
                  key={row.key}
                  id={`${listId}-${i}`}
                  role="option"
                  aria-selected={i === active}
                  onMouseEnter={() => setActive(i)}
                  onClick={() => go(row)}
                  className={clsx('flex cursor-pointer items-center gap-3 px-4 py-2 text-body-sm', i === active ? 'bg-accent-primary-soft text-accent-primary' : 'text-text-primary')}
                >
                  <Icon name={row.icon} size={16} />
                  <span className="min-w-0 flex-1 truncate">{row.label}</span>
                  {row.hint && <span className="flex-shrink-0 text-text-tertiary">{row.hint}</span>}
                </div>
              ))}
            </div>
          ))}
          {rows.length === 0 && !searching && <p className="px-4 py-6 text-center text-body-sm text-text-secondary">Nada encontrado para “{query.trim()}”.</p>}
          {searching && <p className="px-4 py-2 text-body-sm text-text-tertiary">Buscando…</p>}
          {failed && <p className="px-4 py-2 text-body-sm text-status-danger">Não foi possível buscar contatos e conversas agora.</p>}
          {!acting && query.trim().length > 0 && query.trim().length < MIN_REMOTE_QUERY && <p className="px-4 py-2 text-body-sm text-text-tertiary">Digite ao menos {MIN_REMOTE_QUERY} letras para buscar contatos e conversas.</p>}
        </div>
      </div>
    </div>
  )
}
