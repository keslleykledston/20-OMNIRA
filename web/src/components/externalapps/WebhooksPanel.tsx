import { useState } from 'react'
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { Badge, Button, Card, ConfirmDialog, EmptyState, ErrorState, Input, Skeleton } from '../primitives'
import { EVENT_TYPES, describeError, externalAppErrorMessage, externalAppsAPI, validateWebhookURL, type Delivery, type Webhook } from '../../lib/externalApps'
import { getTenantId } from '../../lib/session'
import { SecretOnce } from './SecretOnce'

const when = (iso: string | null) => (iso ? new Date(iso).toLocaleString('pt-BR', { dateStyle: 'short', timeStyle: 'short' }) : '—')

const DELIVERY_LABEL: Record<Delivery['status'], string> = { pending: 'Na fila', delivered: 'Entregue', dead: 'Falhou (fila de erros)', cancelled: 'Cancelada' }

function Deliveries({ appId, hook, canManage, onError }: { appId: string; hook: Webhook; canManage: boolean; onError: (m: string) => void }) {
  const qc = useQueryClient()
  const tenantId = getTenantId()
  const [status, setStatus] = useState<'' | 'dead' | 'pending' | 'delivered'>('dead')
  const list = useQuery({ queryKey: ['external-app-deliveries', tenantId, appId, hook.id, status], queryFn: () => externalAppsAPI.webhooks.deliveries(appId, hook.id, status || undefined) })
  const retry = useMutation({
    mutationFn: (id: string) => externalAppsAPI.webhooks.retry(appId, hook.id, id),
    onSuccess: () => void qc.invalidateQueries({ queryKey: ['external-app-deliveries', tenantId, appId, hook.id] }),
    onError: (e) => onError(externalAppErrorMessage(e)),
  })
  return (
    <div className="space-y-2">
      <div className="flex flex-wrap items-end gap-2">
        <label className="text-xs text-text-secondary">
          <span className="mb-1 block">Entregas</span>
          <select aria-label="Filtrar entregas" className="rounded-control border border-border-subtle bg-surface px-2 py-1 text-sm text-text-primary" value={status} onChange={(e) => setStatus(e.target.value as typeof status)}>
            <option value="dead">Com falha (fila de erros)</option>
            <option value="pending">Na fila</option>
            <option value="delivered">Entregues</option>
            <option value="">Todas</option>
          </select>
        </label>
      </div>
      {list.isLoading && <Skeleton className="h-10" />}
      {list.isError && <ErrorState message="Não foi possível carregar as entregas." action={{ label: 'Tentar novamente', onClick: () => { void list.refetch() } }} />}
      {list.data && list.data.items.length === 0 && <p className="text-xs text-text-tertiary">Nenhuma entrega neste filtro.</p>}
      <ul className="space-y-1">
        {list.data?.items.map((d) => (
          <li key={d.id} className="flex flex-wrap items-center justify-between gap-2 rounded-control bg-surface-muted px-3 py-2 text-xs text-text-secondary">
            <span>
              #{d.event_seq} · {DELIVERY_LABEL[d.status]} · {d.attempts} {d.attempts === 1 ? 'tentativa' : 'tentativas'}
              {d.last_http_status ? ` · HTTP ${d.last_http_status}` : ''}
              {d.last_error_class ? ` · ${describeError(d.last_error_class)}` : ''} · {when(d.last_attempt_at)}
            </span>
            {canManage && (d.status === 'dead' || d.status === 'cancelled') && (
              <Button size="sm" variant="secondary" isLoading={retry.isPending} onClick={() => retry.mutate(d.id)}>Reenviar</Button>
            )}
          </li>
        ))}
      </ul>
    </div>
  )
}

// The endpoints that receive the facts of an installation, signed. The signing secret appears once (creation and rotation).
export default function WebhooksPanel({ appId, canManage, revoked }: { appId: string; canManage: boolean; revoked: boolean }) {
  const qc = useQueryClient()
  const tenantId = getTenantId()
  const key = ['external-app-webhooks', tenantId, appId]
  const list = useQuery({ queryKey: key, queryFn: () => externalAppsAPI.webhooks.list(appId) })
  const [url, setUrl] = useState('')
  const [types, setTypes] = useState<string[]>(EVENT_TYPES.map((t) => t.key))
  const [secret, setSecret] = useState<{ title: string; value: string } | null>(null)
  const [error, setError] = useState<string | null>(null)
  const [removing, setRemoving] = useState<Webhook | null>(null)
  const [open, setOpen] = useState<string | null>(null)
  const refresh = () => void qc.invalidateQueries({ queryKey: key })
  const fail = (e: unknown) => setError(externalAppErrorMessage(e))

  const create = useMutation({
    mutationFn: () => externalAppsAPI.webhooks.create(appId, { url: url.trim(), event_types: types }),
    onSuccess: (r) => {
      setSecret({ title: 'Segredo de assinatura do endpoint', value: r.secret })
      setUrl('')
      setError(null)
      refresh()
    },
    onError: fail,
  })
  const toggle = useMutation({ mutationFn: (h: Webhook) => externalAppsAPI.webhooks.update(appId, h.id, { status: h.status === 'active' ? 'paused' : 'active' }), onSuccess: refresh, onError: fail })
  const rotate = useMutation({
    mutationFn: (h: Webhook) => externalAppsAPI.webhooks.rotate(appId, h.id),
    onSuccess: (r) => {
      setSecret({ title: 'Novo segredo de assinatura', value: r.secret })
      refresh()
    },
    onError: fail,
  })
  const remove = useMutation({ mutationFn: (h: Webhook) => externalAppsAPI.webhooks.remove(appId, h.id), onSuccess: refresh, onError: fail })

  const urlProblem = url.trim() === '' ? null : validateWebhookURL(url)
  const toggleType = (k: string) => setTypes((cur) => (cur.includes(k) ? cur.filter((x) => x !== k) : [...cur, k]))

  return (
    <div className="space-y-3">
      <h3 className="text-sm font-semibold text-text-primary">Endpoints de eventos (webhooks)</h3>
      <p className="text-xs text-text-tertiary">
        O OMNIRA envia cada fato (mensagem nova, mudança de quem atende…) por HTTPS, assinado com HMAC-SHA256. Se o endereço ficar fora do ar, tenta de novo por até 72 horas;
        depois a entrega vai para a fila de erros, de onde pode ser reenviada. No máximo 5 endpoints por aplicação.
      </p>
      {secret && <SecretOnce title={secret.title} secret={secret.value} hint="Guarde agora no seu CRM. Ele não aparece de novo: se perder, gere outro." onDone={() => setSecret(null)} />}
      {error && <ErrorState title="Não foi possível concluir" message={error} isDismissible onDismiss={() => setError(null)} />}
      {list.isLoading && <Skeleton className="h-16" />}
      {list.isError && <ErrorState message="Não foi possível carregar os endpoints." action={{ label: 'Tentar novamente', onClick: () => { void list.refetch() } }} />}
      {list.data && list.data.items.length === 0 && <EmptyState title="Nenhum endpoint" description="Sem endpoint, a aplicação só consegue ler os eventos consultando a API." />}
      <div className="space-y-2">
        {list.data?.items.map((h) => (
          <Card key={h.id} padding="compact" aria-label={`Endpoint ${h.url}`}>
            <div className="space-y-2">
              <div className="flex flex-wrap items-start justify-between gap-2">
                <div className="min-w-0">
                  <p className="break-all text-sm font-medium text-text-primary">{h.url}</p>
                  <p className="text-xs text-text-tertiary">
                    <Badge size="sm" variant={h.status === 'active' ? 'success' : 'warning'}>{h.status === 'active' ? 'Ativo' : 'Pausado'}</Badge>{' '}
                    chave {h.key_id}{h.previous_valid_until ? ` · a chave anterior ainda vale até ${when(h.previous_valid_until)}` : ''} · {h.event_types.length} tipos de evento
                  </p>
                  <p className="text-xs text-text-tertiary">
                    Última entrega: {when(h.last_delivery_at)}{h.last_http_status ? ` (HTTP ${h.last_http_status})` : ''}
                    {h.consecutive_failures > 0 ? ` · ${h.consecutive_failures} falhas seguidas${h.last_error_class ? `: ${describeError(h.last_error_class)}` : ''}` : ''}
                  </p>
                </div>
                <div className="flex flex-wrap gap-2">
                  <Button size="sm" variant="secondary" onClick={() => setOpen(open === h.id ? null : h.id)} aria-expanded={open === h.id}>Entregas</Button>
                  {canManage && !revoked && (
                    <>
                      <Button size="sm" variant="secondary" disabled={toggle.isPending} onClick={() => toggle.mutate(h)}>{h.status === 'active' ? 'Pausar' : 'Retomar'}</Button>
                      <Button size="sm" variant="secondary" disabled={rotate.isPending} onClick={() => rotate.mutate(h)}>Trocar segredo</Button>
                      <Button size="sm" variant="secondary" onClick={() => setRemoving(h)}>Remover</Button>
                    </>
                  )}
                </div>
              </div>
              {open === h.id && <Deliveries appId={appId} hook={h} canManage={canManage && !revoked} onError={setError} />}
            </div>
          </Card>
        ))}
      </div>
      {canManage && !revoked && (
        <Card padding="compact">
          <form className="space-y-3" onSubmit={(e) => { e.preventDefault(); if (!urlProblem && url.trim() && types.length > 0) create.mutate() }}>
            <Input label="Novo endpoint (https)" value={url} placeholder="https://crm.suaempresa.com/omnira/eventos" onChange={(e) => setUrl(e.target.value)} error={urlProblem ?? undefined} />
            <fieldset className="space-y-1">
              <legend className="text-xs font-medium text-text-secondary">Eventos</legend>
              <div className="grid gap-1 sm:grid-cols-2">
                {EVENT_TYPES.map((t) => (
                  <label key={t.key} className="flex items-center gap-2 text-sm text-text-primary">
                    <input type="checkbox" checked={types.includes(t.key)} onChange={() => toggleType(t.key)} /> {t.label}
                  </label>
                ))}
              </div>
            </fieldset>
            <div className="flex justify-end">
              <Button type="submit" size="sm" isLoading={create.isPending} disabled={!url.trim() || !!urlProblem || types.length === 0}>Adicionar endpoint</Button>
            </div>
          </form>
        </Card>
      )}
      <ConfirmDialog open={!!removing} title="Remover este endpoint?" message="As entregas pendentes dele são descartadas. Os eventos continuam disponíveis para consulta pela API." confirmLabel="Remover" destructive
        isPending={remove.isPending} onCancel={() => setRemoving(null)} onConfirm={() => { const h = removing!; setRemoving(null); remove.mutate(h) }} />
    </div>
  )
}
