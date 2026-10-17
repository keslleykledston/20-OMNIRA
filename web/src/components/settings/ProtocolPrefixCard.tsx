import { useState } from 'react'
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { Button, Card, CardBody, CardHeader, Input } from '../primitives'
import { isModuleAbsent, PREFIX_PATTERN, prefixErrorMessage, serviceAPI } from '../../lib/serviceContext'
import { getTenantId } from '../../lib/session'

// The letters that open this company's protocols (KTG-1001). Chosen once, before the first protocol: after that it is
// locked. Shown only to administrators; hidden when the service module is off for the platform.
export default function ProtocolPrefixCard() {
  const tenantId = getTenantId()
  const queryClient = useQueryClient()
  const { data, isLoading, isError, error } = useQuery({
    queryKey: ['service-protocol-prefix', tenantId],
    queryFn: serviceAPI.protocolPrefix,
    retry: false,
  })
  const [draft, setDraft] = useState<string | null>(null)
  const [message, setMessage] = useState<{ kind: 'ok' | 'error'; text: string } | null>(null)

  const save = useMutation({
    mutationFn: serviceAPI.setProtocolPrefix,
    onSuccess: (next) => {
      queryClient.setQueryData(['service-protocol-prefix', tenantId], next)
      setDraft(null)
      setMessage({ kind: 'ok', text: `Prefixo ${next.prefix} reservado. O próximo protocolo será ${next.next_protocol}.` })
    },
    onError: (err) => setMessage({ kind: 'error', text: prefixErrorMessage(err) }),
  })

  if (isError && isModuleAbsent(error)) return null
  const value = draft ?? data?.prefix ?? ''
  const valid = PREFIX_PATTERN.test(value)
  const unchanged = value.toUpperCase() === (data?.prefix ?? '')
  const locked = data?.locked === true

  return (
    <Card>
      <CardHeader>
        <h2 className="text-base font-semibold text-text-primary">Prefixo do protocolo de atendimento</h2>
      </CardHeader>
      <CardBody>
        <p className="mb-4 text-sm text-text-secondary">
          Cada atendimento recebe um protocolo com estas letras e um número a partir de 1001 (por exemplo, KTG-1001).
          {locked ? ' O prefixo já está em uso e não muda mais.' : ' Escolha antes do primeiro protocolo: depois dele o prefixo fica travado.'}
        </p>
        {isLoading ? (
          <p className="text-sm text-text-tertiary">Carregando…</p>
        ) : isError ? (
          <p role="alert" className="text-sm text-status-danger">Não foi possível carregar o prefixo. Recarregue a página.</p>
        ) : (
          <form
            className="flex flex-col gap-4"
            onSubmit={(e) => {
              e.preventDefault()
              if (locked || !valid || unchanged) return
              setMessage(null)
              save.mutate(value.toUpperCase())
            }}
          >
            <Input
              id="protocol-prefix"
              label="Prefixo"
              value={value}
              maxLength={6}
              disabled={locked || save.isPending}
              onChange={(e) => {
                setDraft(e.target.value.replace(/[^A-Za-z]/g, '').toUpperCase())
                setMessage(null)
              }}
              helperText={data?.configured ? 'Prefixo reservado.' : 'Sugestão a partir do nome da empresa; vira definitivo ao salvar ou no primeiro protocolo.'}
            />
            <p className="text-xs text-text-secondary" aria-live="polite">
              Próximo protocolo: <span className="font-medium text-text-primary">{valid ? `${value.toUpperCase()}-${nextNumber(data?.next_protocol)}` : '—'}</span>
            </p>
            {message && (
              <p role={message.kind === 'error' ? 'alert' : 'status'} className={`text-sm ${message.kind === 'error' ? 'text-status-danger' : 'text-status-success'}`}>
                {message.text}
              </p>
            )}
            {!locked && (
              <div>
                <Button type="submit" isLoading={save.isPending} disabled={!valid || unchanged || save.isPending}>
                  Reservar prefixo
                </Button>
              </div>
            )}
          </form>
        )}
      </CardBody>
    </Card>
  )
}

// "KTG-1005" -> "1005"; the number part of whatever the server proposes.
function nextNumber(next?: string): string {
  const n = next?.split('-').pop()
  return n && /^\d+$/.test(n) ? n : '1001'
}
