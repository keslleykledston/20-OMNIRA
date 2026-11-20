import { useRef, useState } from 'react'
import { useMutation, useQueryClient } from '@tanstack/react-query'
import { Button, ConfirmDialog, StatusBadge } from '../../primitives'
import { groupAiAPI, groupAiErrorMessage, type GroupImport } from '../../../lib/groupAi'
import {
  DISCARD_REASON_LABEL,
  IMPORT_STATUS_LABEL,
  canDiscardImport,
  coverageVerdict,
  discardHint,
  formatBytes,
  formatDay,
  formatWhen,
  importInProgress,
  rejectedReasonsText,
  validateImportFile,
} from '../../../lib/groupAiModel'
import { showToast } from '../../../lib/toast'
import { groupAiKey, useGroupImports } from '../../../lib/useGroupAi'
import { Notice, SectionTitle } from './ui'

interface Props {
  groupId: string
  canImport: boolean
  aiEnabled: boolean
  onOpenSettings: () => void
}

const statusTone = { uploaded: 'info', extracting: 'info', extracted: 'success', failed: 'danger', discarded: 'default' } as const

// Import of a conversation export (.txt/.zip). The raw file is never shown or downloaded: only its metadata and the COVERAGE record.
// "Coverage complete" is worded as "everything was read", never as "everything was captured" (ADR-0050 §6).
export function ImportsTab({ groupId, canImport, aiEnabled, onOpenSettings }: Props) {
  const qc = useQueryClient()
  const fileRef = useRef<HTMLInputElement>(null)
  const [error, setError] = useState<string | null>(null)
  const [discarding, setDiscarding] = useState<GroupImport | null>(null)
  const imports = useGroupImports(groupId, true)

  const refresh = () => void qc.invalidateQueries({ queryKey: groupAiKey(groupId) })
  const fail = (action: Parameters<typeof groupAiErrorMessage>[1]) => (err: unknown) => {
    setError(groupAiErrorMessage(err, action))
    refresh()
  }

  const upload = useMutation({
    mutationFn: (file: File) => groupAiAPI.uploadImport(groupId, file),
    onSuccess: () => { setError(null); refresh(); showToast('Arquivo recebido. A leitura acontece em segundo plano e o resultado aparece aqui.', 'info') },
    onError: fail('upload'),
  })
  const discard = useMutation({
    mutationFn: (i: GroupImport) => groupAiAPI.discardImport(groupId, i.id),
    onSuccess: () => { setError(null); setDiscarding(null); refresh(); showToast('Arquivo original descartado. O registro de cobertura continua.') },
    onError: (err) => { setDiscarding(null); fail('discard')(err) },
  })
  const reprocess = useMutation({
    mutationFn: (i: GroupImport) => groupAiAPI.reprocessImport(groupId, i.id),
    onSuccess: () => { setError(null); refresh(); showToast('Reprocessamento pedido. A leitura acontece em segundo plano.', 'info') },
    onError: fail('reprocess'),
  })

  const onPick = (file: File | undefined) => {
    if (fileRef.current) fileRef.current.value = '' // the same file can be picked again after a refusal
    if (!file) return
    const problem = validateImportFile(file)
    if (problem) {
      setError(problem)
      return
    }
    setError(null)
    upload.mutate(file)
  }

  const items = imports.data?.items ?? []

  return (
    <div className="space-y-4 p-4">
      <p className="text-xs leading-relaxed text-text-secondary">
        Traga a conversa antiga do grupo (exportação do WhatsApp em .txt ou .zip) para a IA aprender com ela. Só o texto é lido: fotos, áudios e outros arquivos
        do .zip são ignorados. O arquivo é guardado cifrado, por padrão por 90 dias, e depois descartado. Ele não entra no histórico de mensagens do grupo.
      </p>

      {canImport && (
        <div className="space-y-2">
          <input
            ref={fileRef}
            type="file"
            accept=".txt,.zip,text/plain,application/zip"
            aria-label="Arquivo da conversa (.txt ou .zip)"
            className="sr-only"
            tabIndex={-1}
            onChange={(e) => onPick(e.target.files?.[0])}
          />
          <Button variant="primary" size="sm" disabled={!aiEnabled} isLoading={upload.isPending} onClick={() => fileRef.current?.click()}>
            Importar conversa (.txt ou .zip)
          </Button>
          {upload.isPending && <p role="status" className="text-xs text-text-secondary">Enviando e verificando o arquivo...</p>}
          {!aiEnabled && (
            <p className="text-xs text-text-tertiary">
              Para importar, a IA precisa estar ligada neste grupo.{' '}
              <button type="button" onClick={onOpenSettings} className="font-medium text-accent-primary underline">
                Ir para Ajustes
              </button>
            </p>
          )}
        </div>
      )}

      {error && <Notice tone="danger" role="alert">{error}</Notice>}
      {imports.isLoading && <p className="py-4 text-center text-sm text-text-secondary">Carregando importações...</p>}
      {imports.isError && (
        <p role="alert" className="text-sm text-status-danger">
          {groupAiErrorMessage(imports.error, 'load')}
        </p>
      )}

      {imports.data && items.length === 0 && (
        <p className="rounded-card border border-dashed border-border-subtle px-3 py-6 text-center text-sm text-text-secondary">Nenhum arquivo foi importado neste grupo.</p>
      )}

      <ul className="space-y-3" aria-label="Arquivos importados">
        {items.map((imp) => (
          <ImportCard
            key={imp.id}
            imp={imp}
            canImport={canImport}
            aiEnabled={aiEnabled}
            busy={discard.isPending || reprocess.isPending}
            onDiscard={() => setDiscarding(imp)}
            onReprocess={() => reprocess.mutate(imp)}
          />
        ))}
      </ul>

      <ConfirmDialog
        open={!!discarding}
        title="Descartar o arquivo original?"
        message="O arquivo guardado é apagado e não poderá mais ser reprocessado. Os itens da memória e o registro de cobertura continuam. Cópias de segurança já feitas podem conter o arquivo até expirarem."
        confirmLabel="Descartar arquivo original"
        destructive
        isPending={discard.isPending}
        onConfirm={() => discarding && discard.mutate(discarding)}
        onCancel={() => setDiscarding(null)}
      />
    </div>
  )
}

function Stat({ label, value }: { label: string; value: string | number }) {
  return (
    <div className="min-w-0">
      <dt className="text-[11px] text-text-tertiary">{label}</dt>
      <dd className="text-sm font-medium text-text-primary">{value}</dd>
    </div>
  )
}

function ImportCard({
  imp,
  canImport,
  aiEnabled,
  busy,
  onDiscard,
  onReprocess,
}: {
  imp: GroupImport
  canImport: boolean
  aiEnabled: boolean
  busy: boolean
  onDiscard: () => void
  onReprocess: () => void
}) {
  const verdict = coverageVerdict(imp)
  const rejected = rejectedReasonsText(imp.rejected_reasons)
  const hint = discardHint(imp)
  const range = imp.range_from && imp.range_to ? `${formatDay(imp.range_from)} a ${formatDay(imp.range_to)}` : 'não informado'

  return (
    <li className="space-y-3 rounded-card border border-border-subtle bg-surface p-3">
      <div className="flex flex-wrap items-start justify-between gap-2">
        <div className="min-w-0">
          <p className="truncate text-sm font-semibold text-text-primary">{imp.source_filename || 'Arquivo sem nome'}</p>
          <p className="text-xs text-text-secondary">
            {imp.format === 'whatsapp_zip' ? 'ZIP' : 'TXT'} · {formatBytes(imp.size_bytes)} · enviado em {formatWhen(imp.created_at)}
          </p>
        </div>
        <StatusBadge size="sm" status={statusTone[imp.status]}>
          {IMPORT_STATUS_LABEL[imp.status]}
        </StatusBadge>
      </div>

      <section aria-label="Registro de cobertura" className="space-y-2 rounded-control bg-surface-muted p-3">
        <SectionTitle>Registro de cobertura</SectionTitle>
        <Notice tone={verdict.tone} role="status">
          <strong className="font-semibold">{verdict.title}.</strong> {verdict.detail}
        </Notice>
        <dl className="grid grid-cols-2 gap-x-3 gap-y-2 sm:grid-cols-3">
          <Stat label="Mensagens no arquivo" value={imp.messages_total} />
          <Stat label="Entendidas" value={imp.messages_parsed} />
          <Stat label="Rejeitadas" value={imp.messages_rejected ?? 0} />
          <Stat label="Já estavam no histórico" value={imp.messages_duplicate ?? 0} />
          <Stat label="Lotes lidos" value={`${imp.batches_ok ?? 0} de ${imp.batches_total ?? 0}`} />
          <Stat label="Lotes com falha" value={imp.batches_failed ?? 0} />
          <Stat label="Itens registrados" value={imp.items_created ?? 0} />
          <Stat label="Período" value={range} />
        </dl>
        <p className="text-xs text-text-secondary">
          O total inclui as linhas do sistema do WhatsApp.{rejected ? ` Rejeitadas: ${rejected}.` : ''}
        </p>
      </section>

      <p className="text-xs text-text-secondary">
        {imp.raw_available
          ? `Arquivo original guardado, cifrado, até ${formatDay(imp.expires_at)}.`
          : `Arquivo original descartado${imp.discarded_at ? ` em ${formatDay(imp.discarded_at)}` : ''}${imp.discard_reason ? `: ${DISCARD_REASON_LABEL[imp.discard_reason]}` : ''}.`}
      </p>

      {canImport && imp.raw_available && (
        <div className="space-y-1.5 border-t border-border-subtle pt-2">
          <div className="flex flex-wrap gap-2">
            <Button size="sm" variant="secondary" disabled={!canDiscardImport(imp) || busy} onClick={onDiscard}>
              Descartar arquivo original
            </Button>
            <Button size="sm" variant="secondary" disabled={importInProgress(imp) || !aiEnabled || busy} onClick={onReprocess}>
              Reprocessar
            </Button>
          </div>
          {hint && <p className="text-xs text-text-tertiary">{hint}</p>}
          <p className="text-xs text-text-tertiary">Reprocessar lê o arquivo guardado de novo. Itens confirmados ou fixados por pessoas nunca são sobrescritos.</p>
        </div>
      )}
    </li>
  )
}
