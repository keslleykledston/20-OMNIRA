import { useEffect, useRef, useState } from 'react'
import { useMutation, useQueryClient } from '@tanstack/react-query'
import { Button, Card, CardBody, CardHeader, Input, PageHeader, PermissionState } from '../components/primitives'
import { useInboxSettings } from '../hooks/useInboxSettings'
import {
  DEFAULT_INBOX_SETTINGS,
  inboxSettingsAPI,
  inboxSettingsErrorMessage,
  validateWaitThresholds,
} from '../lib/inboxSettings'
import { getTenantId } from '../lib/session'
import { useAccess } from '../lib/useAccess'

// "30 min", "2 h", "3 d": the same compact units the conversation list uses.
export function formatMinutes(m: number): string {
  if (m < 60) return `${m} min`
  if (m < 1440 && m % 60 === 0) return `${m / 60} h`
  if (m % 1440 === 0) return `${m / 1440} d`
  return `${m} min`
}

const WHOLE_NUMBER = /^\d+$/

// Organisation preferences. Today one section: when the "customer is waiting" chip in the Inbox list
// turns yellow and red. Everyone can read it (the Inbox needs it); only an administrator saves, and
// the API refuses anyone else regardless of what this page shows.
export default function SettingsPage() {
  const tenantId = getTenantId()
  const access = useAccess()
  const queryClient = useQueryClient()
  const { settings, isLoading, isError } = useInboxSettings()
  const accessReady = !access.isLoading
  const canManage = access.can('tenant.manage')

  // Raw text drafts: clearing a field to type a new number must not snap to a value.
  const [warn, setWarn] = useState(String(settings.wait_warn_minutes))
  const [danger, setDanger] = useState(String(settings.wait_danger_minutes))
  const [saved, setSaved] = useState(false)
  const [saveError, setSaveError] = useState<string | null>(null)
  const seeded = useRef(false)

  // Fill the form once from the server value; later background refetches never overwrite what is being typed.
  useEffect(() => {
    if (!isLoading && !isError && !seeded.current) {
      seeded.current = true
      setWarn(String(settings.wait_warn_minutes))
      setDanger(String(settings.wait_danger_minutes))
    }
  }, [isLoading, isError, settings])

  const save = useMutation({
    mutationFn: inboxSettingsAPI.update,
    onSuccess: (next) => {
      setWarn(String(next.wait_warn_minutes))
      setDanger(String(next.wait_danger_minutes))
      setSaveError(null)
      setSaved(true)
      void queryClient.invalidateQueries({ queryKey: ['inbox-settings', tenantId] })
    },
    onError: (err) => {
      setSaved(false)
      setSaveError(inboxSettingsErrorMessage(err))
    },
  })

  const dirty = warn !== String(settings.wait_warn_minutes) || danger !== String(settings.wait_danger_minutes)
  const validation =
    !WHOLE_NUMBER.test(warn) || !WHOLE_NUMBER.test(danger)
      ? 'Informe números inteiros de minutos (mínimo 1).'
      : validateWaitThresholds(Number(warn), Number(danger))
  const preview = validation === null ? { warn: Number(warn), danger: Number(danger) } : null
  const isDefault = warn === String(DEFAULT_INBOX_SETTINGS.wait_warn_minutes) && danger === String(DEFAULT_INBOX_SETTINGS.wait_danger_minutes)

  const edit = (setter: (v: string) => void) => (value: string) => {
    setter(value)
    setSaved(false)
    setSaveError(null)
  }

  if (!access.isLoading && !access.can('tenant.read')) {
    return (
      <div className="px-6 py-6 lg:px-8 lg:py-8">
        <PermissionState message="Você não tem permissão para ver as configurações." />
      </div>
    )
  }

  return (
    <div>
      <PageHeader title="Configurações" description="Preferências da organização." />
      <div className="px-6 py-6 lg:px-8 lg:py-8 max-w-3xl">
        <Card>
          <CardHeader>
            <h2 className="text-base font-semibold text-text-primary">Tempo de espera na lista de conversas</h2>
          </CardHeader>
          <CardBody>
            <p className="mb-5 text-sm text-text-secondary">
              Quando um cliente escreve e ninguém responde, a lista de conversas mostra há quanto tempo ele espera. Defina
              quando essa ficha muda de cor. São só indicadores visuais: nada é bloqueado nem escalado a partir deles.
            </p>

            {isError ? (
              <p role="alert" className="text-sm text-status-danger">
                Não foi possível carregar as configurações. Recarregue a página.
              </p>
            ) : (
              <form
                onSubmit={(e) => {
                  e.preventDefault()
                  if (!canManage || validation !== null || !dirty) return
                  save.mutate({ wait_warn_minutes: Number(warn), wait_danger_minutes: Number(danger) })
                }}
                className="flex flex-col gap-5"
              >
                <div className="grid grid-cols-1 gap-4 sm:grid-cols-2">
                  <Input
                    id="wait-warn"
                    label="Atenção (amarelo) após"
                    inputMode="numeric"
                    value={warn}
                    disabled={!accessReady || !canManage || isLoading}
                    onChange={(e) => edit(setWarn)(e.target.value)}
                    helperText="minutos esperando"
                  />
                  <Input
                    id="wait-danger"
                    label="Crítico (vermelho) após"
                    inputMode="numeric"
                    value={danger}
                    disabled={!accessReady || !canManage || isLoading}
                    onChange={(e) => edit(setDanger)(e.target.value)}
                    helperText="minutos esperando (máximo 10080, 7 dias)"
                  />
                </div>

                <div className="flex flex-wrap items-center gap-2 text-xs text-text-secondary" aria-live="polite">
                  <span>Na lista:</span>
                  {preview ? (
                    <>
                      <span className="rounded-pill bg-surface-muted px-2 py-0.5 text-text-secondary">menos de {formatMinutes(preview.warn)}</span>
                      <span className="rounded-pill bg-status-warning-soft px-2 py-0.5 text-status-warning">
                        {formatMinutes(preview.warn)} a {formatMinutes(preview.danger)}
                      </span>
                      <span className="rounded-pill bg-status-danger-soft px-2 py-0.5 text-status-danger">
                        a partir de {formatMinutes(preview.danger)}
                      </span>
                    </>
                  ) : (
                    <span>—</span>
                  )}
                </div>

                {validation && dirty && (
                  <p role="alert" className="text-sm text-status-danger">
                    {validation}
                  </p>
                )}
                {saveError && (
                  <p role="alert" className="text-sm text-status-danger">
                    {saveError}
                  </p>
                )}
                {saved && <p role="status" className="text-sm text-status-success">Salvo.</p>}

                {!accessReady ? null : canManage ? (
                  <div className="flex flex-wrap items-center gap-3">
                    <Button type="submit" isLoading={save.isPending} disabled={!dirty || validation !== null || save.isPending}>
                      Salvar
                    </Button>
                    <Button
                      type="button"
                      variant="tertiary"
                      disabled={isDefault || save.isPending}
                      onClick={() => {
                        edit(setWarn)(String(DEFAULT_INBOX_SETTINGS.wait_warn_minutes))
                        edit(setDanger)(String(DEFAULT_INBOX_SETTINGS.wait_danger_minutes))
                      }}
                    >
                      Restaurar padrão (30 min e 2 h)
                    </Button>
                  </div>
                ) : (
                  <p className="text-sm text-text-tertiary">Somente administradores podem alterar estes limites.</p>
                )}
              </form>
            )}
          </CardBody>
        </Card>
      </div>
    </div>
  )
}
