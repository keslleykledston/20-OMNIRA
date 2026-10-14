import { useState } from 'react'
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { Badge, Button, Card, ConfirmDialog, Drawer, EmptyState, ErrorState, Input, PermissionState, Skeleton } from '../components/primitives'
import { Field, Notice } from '../components/attendance/AttendanceFrame'
import { SettingsShell, SETTINGS_SECTIONS } from '../components/SettingsShell'
import { SecretOnce } from '../components/externalapps/SecretOnce'
import WebhooksPanel from '../components/externalapps/WebhooksPanel'
import { channelsAPI } from '../lib/channels'
import { SCOPES, externalAppErrorMessage, externalAppsAPI, statusLabel, type ExternalApp, type IssuedCredential } from '../lib/externalApps'
import { queuesAPI } from '../lib/queues'
import { getTenantId } from '../lib/session'
import { useAccess } from '../lib/useAccess'

const when = (iso: string | null | undefined) => (iso ? new Date(iso).toLocaleString('pt-BR', { dateStyle: 'short', timeStyle: 'short' }) : '—')
const STATUS_VARIANT = { active: 'success', suspended: 'warning', revoked: 'danger' } as const

function isNotEnabled(err: unknown): boolean {
  return (err as { response?: { status?: number } })?.response?.status === 404
}

function CreateForm({ onCreated, onClose }: { onCreated: (app: ExternalApp, cred: IssuedCredential) => void; onClose: () => void }) {
  const tenantId = getTenantId()
  const qc = useQueryClient()
  const [name, setName] = useState('')
  const [scopes, setScopes] = useState<string[]>(['conversation.read', 'message.read', 'contact.read', 'events.read'])
  const [queueIds, setQueueIds] = useState<string[]>([])
  const [channelIds, setChannelIds] = useState<string[]>([])
  const [outside, setOutside] = useState(false)
  const [days, setDays] = useState('90')
  const [error, setError] = useState<string | null>(null)
  const queues = useQuery({ queryKey: ['queues', tenantId], queryFn: queuesAPI.list })
  const channels = useQuery({ queryKey: ['channel-connections', tenantId], queryFn: channelsAPI.list })
  const create = useMutation({
    mutationFn: () => externalAppsAPI.create({ name: name.trim(), scopes, queue_ids: queueIds, channel_ids: channelIds, allow_outside_hours: outside, credential_ttl_days: Number(days) }),
    onSuccess: (r) => {
      void qc.invalidateQueries({ queryKey: ['external-apps', tenantId] })
      onCreated(r.installation, r.credential)
    },
    onError: (e) => setError(externalAppErrorMessage(e)),
  })
  const toggle = (list: string[], set: (v: string[]) => void, k: string) => set(list.includes(k) ? list.filter((x) => x !== k) : [...list, k])
  const writes = scopes.some((s) => SCOPES.find((x) => x.key === s)?.write)
  const daysOk = /^\d+$/.test(days) && Number(days) >= 1 && Number(days) <= 365
  const valid = name.trim().length > 0 && scopes.length > 0 && daysOk
  return (
    <form className="space-y-4" onSubmit={(e) => { e.preventDefault(); if (valid) create.mutate() }}>
      {error && <ErrorState title="Não foi possível criar" message={error} isDismissible onDismiss={() => setError(null)} />}
      <Input label="Nome da aplicação" value={name} maxLength={80} placeholder="CRM da empresa" onChange={(e) => setName(e.target.value)} />
      <fieldset className="space-y-1">
        <legend className="text-xs font-medium text-text-secondary">O que a aplicação pode fazer</legend>
        {SCOPES.map((s) => (
          <label key={s.key} className="flex items-center gap-2 text-sm text-text-primary">
            <input type="checkbox" checked={scopes.includes(s.key)} onChange={() => toggle(scopes, setScopes, s.key)} /> {s.label} {s.write && <Badge size="sm" variant="warning">escreve</Badge>}
          </label>
        ))}
      </fieldset>
      {writes && <Notice tone="warning">Com permissões de escrita a aplicação responde clientes e assume conversas. A escrita só funciona se estiver habilitada neste ambiente.</Notice>}
      <fieldset className="space-y-1">
        <legend className="text-xs font-medium text-text-secondary">Filas que a aplicação enxerga <span className="text-text-tertiary">(vazio = todas)</span></legend>
        {queues.data?.map((q) => (
          <label key={q.id} className="flex items-center gap-2 text-sm text-text-primary"><input type="checkbox" checked={queueIds.includes(q.id)} onChange={() => toggle(queueIds, setQueueIds, q.id)} /> {q.name}</label>
        ))}
        {queues.isError && <p className="text-xs text-text-tertiary">Não foi possível listar as filas.</p>}
      </fieldset>
      <fieldset className="space-y-1">
        <legend className="text-xs font-medium text-text-secondary">Linhas (canais) que a aplicação enxerga <span className="text-text-tertiary">(vazio = todas)</span></legend>
        {channels.data?.map((c) => (
          <label key={c.id} className="flex items-center gap-2 text-sm text-text-primary"><input type="checkbox" checked={channelIds.includes(c.id)} onChange={() => toggle(channelIds, setChannelIds, c.id)} /> {c.external_account_id ?? c.provider}</label>
        ))}
        {channels.isError && <p className="text-xs text-text-tertiary">Não foi possível listar as linhas.</p>}
      </fieldset>
      <label className="flex items-center gap-2 text-sm text-text-primary"><input type="checkbox" checked={outside} onChange={() => setOutside(!outside)} /> Pode assumir conversas fora do horário do setor</label>
      <Input label="Validade do token (dias)" inputMode="numeric" value={days} onChange={(e) => setDays(e.target.value)} error={daysOk ? undefined : 'Informe de 1 a 365 dias.'} />
      <div className="flex justify-end gap-2">
        <Button variant="secondary" type="button" onClick={onClose}>Cancelar</Button>
        <Button type="submit" isLoading={create.isPending} disabled={!valid}>Criar aplicação</Button>
      </div>
    </form>
  )
}

function Detail({ appId, canManage, onChanged }: { appId: string; canManage: boolean; onChanged: () => void }) {
  const tenantId = getTenantId()
  const qc = useQueryClient()
  const app = useQuery({ queryKey: ['external-app', tenantId, appId], queryFn: () => externalAppsAPI.get(appId) })
  const [issued, setIssued] = useState<IssuedCredential | null>(null)
  const [error, setError] = useState<string | null>(null)
  const [confirm, setConfirm] = useState<'suspend' | 'revoke' | null>(null)
  const refresh = () => {
    void qc.invalidateQueries({ queryKey: ['external-app', tenantId, appId] })
    void qc.invalidateQueries({ queryKey: ['external-apps', tenantId] })
    onChanged()
  }
  const fail = (e: unknown) => setError(externalAppErrorMessage(e))
  const newToken = useMutation({ mutationFn: () => externalAppsAPI.issueCredential(appId, { label: 'rotação', ttl_days: 90 }), onSuccess: (c) => { setIssued(c); refresh() }, onError: fail })
  const revokeToken = useMutation({ mutationFn: (id: string) => externalAppsAPI.revokeCredential(appId, id), onSuccess: refresh, onError: fail })
  const transition = useMutation({
    mutationFn: (kind: 'suspend' | 'reactivate' | 'revoke') => (kind === 'suspend' ? externalAppsAPI.suspend(appId) : kind === 'reactivate' ? externalAppsAPI.reactivate(appId) : externalAppsAPI.revoke(appId)),
    onSuccess: refresh,
    onError: fail,
  })
  if (app.isLoading) return <Skeleton className="h-40" />
  if (app.isError || !app.data) return <ErrorState message="Não foi possível carregar a aplicação." action={{ label: 'Tentar novamente', onClick: () => { void app.refetch() } }} />
  const a = app.data
  const live = a.status !== 'revoked'
  return (
    <div className="space-y-5">
      {error && <ErrorState title="Não foi possível concluir" message={error} isDismissible onDismiss={() => setError(null)} />}
      <div className="space-y-1 text-sm text-text-secondary">
        <p><Badge variant={STATUS_VARIANT[a.status]}>{statusLabel(a.status)}</Badge> · criada em {when(a.created_at)}</p>
        <p>Limite: {a.rate_limit_per_min} chamadas por minuto · {a.allow_outside_hours ? 'pode assumir fora do horário' : 'só assume dentro do horário do setor'}</p>
        <p>Pode: {a.scopes.map((s) => SCOPES.find((x) => x.key === s)?.label ?? s).join('; ')}</p>
        <p>Filas: {a.queue_ids.length === 0 ? 'todas' : `${a.queue_ids.length} selecionada(s)`} · Linhas: {a.channel_ids.length === 0 ? 'todas' : `${a.channel_ids.length} selecionada(s)`}</p>
      </div>
      {canManage && live && (
        <div className="flex flex-wrap gap-2">
          {a.status === 'active' ? <Button size="sm" variant="secondary" onClick={() => setConfirm('suspend')}>Suspender</Button> : <Button size="sm" variant="secondary" disabled={transition.isPending} onClick={() => transition.mutate('reactivate')}>Reativar</Button>}
          <Button size="sm" variant="danger" onClick={() => setConfirm('revoke')}>Revogar</Button>
        </div>
      )}
      <section className="space-y-2">
        <h3 className="text-sm font-semibold text-text-primary">Tokens de acesso</h3>
        {issued && <SecretOnce title="Novo token de acesso" secret={issued.token} hint="Use no cabeçalho Authorization: Bearer. Ele não aparece de novo: se perder, gere outro." onDone={() => setIssued(null)} />}
        <ul className="space-y-1">
          {a.credentials?.map((c) => (
            <li key={c.id} className="flex flex-wrap items-center justify-between gap-2 rounded-control bg-surface-muted px-3 py-2 text-xs text-text-secondary">
              <span>{c.key_id} · {c.label || 'sem rótulo'} · {c.live ? 'vale até ' + when(c.expires_at) : c.revoked_at ? 'revogado' : 'expirado'} · último uso {when(c.last_used_at)}</span>
              {canManage && c.live && <Button size="sm" variant="secondary" disabled={revokeToken.isPending} onClick={() => revokeToken.mutate(c.id)}>Revogar token</Button>}
            </li>
          ))}
        </ul>
        {canManage && live && <Button size="sm" variant="secondary" isLoading={newToken.isPending} onClick={() => newToken.mutate()}>Gerar novo token</Button>}
      </section>
      <WebhooksPanel appId={a.id} canManage={canManage} revoked={!live} />
      <ConfirmDialog open={confirm === 'suspend'} title="Suspender esta aplicação?" message="Ela deixa de acessar a API e devolve à empresa as conversas que está atendendo. Dá para reativar depois." confirmLabel="Suspender" isPending={transition.isPending}
        onCancel={() => setConfirm(null)} onConfirm={() => { setConfirm(null); transition.mutate('suspend') }} />
      <ConfirmDialog open={confirm === 'revoke'} title="Revogar esta aplicação?" message="Definitivo: os tokens deixam de valer, os endpoints param e as conversas que ela atende voltam para a fila. Não dá para desfazer." confirmLabel="Revogar" destructive isPending={transition.isPending}
        onCancel={() => setConfirm(null)} onConfirm={() => { setConfirm(null); transition.mutate('revoke') }} />
    </div>
  )
}

// Configurações > Integrações > Aplicações externas (ADR-0046): quem instala, revoga e acompanha os CRMs que usam a API headless.
export default function ExternalAppsPage() {
  const tenantId = getTenantId()
  const access = useAccess()
  const read = access.can('integration.read')
  const manage = access.can('integration.manage')
  const [creating, setCreating] = useState(false)
  const [created, setCreated] = useState<{ app: ExternalApp; cred: IssuedCredential } | null>(null)
  const [selected, setSelected] = useState<string | null>(null)
  const list = useQuery({ queryKey: ['external-apps', tenantId], queryFn: externalAppsAPI.list, enabled: read, retry: false })
  const section = SETTINGS_SECTIONS.find((s) => s.key === 'integrations')
  const disabled = list.isError && isNotEnabled(list.error)
  return (
    <SettingsShell sections={SETTINGS_SECTIONS} subsections={section?.subsections} title="Aplicações externas"
      description="CRMs e sistemas que atendem as mesmas conversas do OMNIRA pela API. Cada aplicação tem seus próprios tokens, o que pode ver e o que pode fazer."
      actions={manage && !disabled ? <Button onClick={() => setCreating(true)}>+ Nova aplicação</Button> : undefined}>
      {!access.isLoading && !read ? (
        <PermissionState message="Você não tem permissão para ver as aplicações externas." />
      ) : (
        <div className="space-y-4">
          {created && <SecretOnce title={`Token de acesso de ${created.app.name}`} secret={created.cred.token} hint="Use no cabeçalho Authorization: Bearer. Ele não aparece de novo: se perder, gere outro." onDone={() => setCreated(null)} />}
          {list.isLoading && <Skeleton className="h-24" />}
          {disabled && <EmptyState title="Recurso não habilitado neste ambiente" description="A API para aplicações externas está desligada. Peça ao responsável pela plataforma para habilitá-la antes de instalar uma aplicação." />}
          {list.isError && !disabled && <ErrorState message="Não foi possível carregar as aplicações." action={{ label: 'Tentar novamente', onClick: () => { void list.refetch() } }} />}
          {list.data && list.data.items.length === 0 && <EmptyState title="Nenhuma aplicação instalada" description="Instale uma aplicação para receber os eventos e responder clientes por outro sistema." />}
          <div className="grid gap-3">
            {list.data?.items.map((a) => (
              <Card key={a.id} padding="compact" aria-label={`Aplicação ${a.name}`}>
                <div className="flex flex-wrap items-start justify-between gap-3">
                  <div className="min-w-0">
                    <h2 className="text-base font-semibold text-text-primary">{a.name} <Badge size="sm" variant={STATUS_VARIANT[a.status]}>{statusLabel(a.status)}</Badge></h2>
                    <p className="text-xs text-text-tertiary">{a.scopes.length} permissões · criada em {when(a.created_at)}</p>
                  </div>
                  <Button size="sm" variant="secondary" onClick={() => setSelected(a.id)}>{manage ? 'Gerenciar' : 'Ver'}</Button>
                </div>
              </Card>
            ))}
          </div>
          <Drawer open={creating} title="Nova aplicação externa" onClose={() => setCreating(false)}>
            {creating && <CreateForm onClose={() => setCreating(false)} onCreated={(app, cred) => { setCreating(false); setCreated({ app, cred }) }} />}
          </Drawer>
          <Drawer open={!!selected} title="Aplicação externa" onClose={() => setSelected(null)}>
            {selected && <Detail key={selected} appId={selected} canManage={manage} onChanged={() => void list.refetch()} />}
          </Drawer>
        </div>
      )}
    </SettingsShell>
  )
}
