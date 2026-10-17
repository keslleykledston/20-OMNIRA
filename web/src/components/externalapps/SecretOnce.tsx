import { useState } from 'react'
import { Button } from '../primitives'

// A token or a signing secret is shown once: the page keeps it only while this panel is open, and nothing here writes it to storage or to the address.
export function SecretOnce({ title, secret, hint, onDone }: { title: string; secret: string; hint: string; onDone: () => void }) {
  const [copied, setCopied] = useState<'yes' | 'no' | null>(null)
  const copy = async () => {
    try {
      await navigator.clipboard.writeText(secret)
      setCopied('yes')
    } catch {
      setCopied('no')
    }
  }
  return (
    <div role="alert" className="space-y-3 rounded-card border border-status-warning-border bg-status-warning-soft p-4">
      <p className="text-sm font-semibold text-text-primary">{title}</p>
      <p className="text-xs text-text-secondary">{hint}</p>
      <code data-testid="secret-once" className="block break-all rounded-control bg-surface p-3 text-xs text-text-primary select-all">{secret}</code>
      <div className="flex flex-wrap items-center gap-2">
        <Button size="sm" variant="secondary" onClick={() => void copy()}>Copiar</Button>
        {copied === 'yes' && <span className="text-xs text-text-secondary">Copiado.</span>}
        {copied === 'no' && <span className="text-xs text-text-secondary">Não foi possível copiar: selecione o texto acima.</span>}
        <Button size="sm" onClick={onDone}>Já guardei, fechar</Button>
      </div>
    </div>
  )
}
