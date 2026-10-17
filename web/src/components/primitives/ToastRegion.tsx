import { useCallback, useEffect, useRef, useState } from 'react'
import clsx from 'clsx'
import { TOAST_EVENT, TOAST_MS, type ToastDetail } from '../../lib/toast'
import { Icon } from './Icon'

const MAX_VISIBLE = 3

const KIND_STYLE: Record<ToastDetail['kind'], string> = {
  success: 'border-status-success-border bg-status-success-soft text-status-success',
  error: 'border-status-danger-border bg-status-danger-soft text-status-danger',
  info: 'border-status-info-border bg-status-info-soft text-status-info',
}

/**
 * Onde os avisos de `showToast` aparecem. Monte UMA vez por shell (Layout, console). Região `aria-live`: sucesso e informação em
 * `status` (educado), erro em `alert` (assertivo). Sem dependência nova.
 */
export function ToastRegion() {
  const [items, setItems] = useState<ToastDetail[]>([])
  const timers = useRef(new Map<number, number>())

  const dismiss = useCallback((id: number) => {
    const t = timers.current.get(id)
    if (t !== undefined) window.clearTimeout(t)
    timers.current.delete(id)
    setItems((cur) => cur.filter((i) => i.id !== id))
  }, [])

  useEffect(() => {
    const timersRef = timers.current
    const on = (e: Event) => {
      const d = (e as CustomEvent<ToastDetail>).detail
      if (!d) return
      setItems((cur) => [...cur, d].slice(-MAX_VISIBLE))
      if (d.kind !== 'error') timersRef.set(d.id, window.setTimeout(() => dismiss(d.id), TOAST_MS))
    }
    window.addEventListener(TOAST_EVENT, on)
    return () => {
      window.removeEventListener(TOAST_EVENT, on)
      timersRef.forEach((t) => window.clearTimeout(t))
      timersRef.clear()
    }
  }, [dismiss])

  return (
    <div className="pointer-events-none fixed bottom-20 right-4 z-[60] flex w-[min(24rem,calc(100vw-2rem))] flex-col gap-2 lg:bottom-6">
      {items.map((t) => (
        <div
          key={t.id}
          role={t.kind === 'error' ? 'alert' : 'status'}
          className={clsx('pointer-events-auto flex items-start gap-2 rounded-control border px-3 py-2 text-sm shadow-md', KIND_STYLE[t.kind])}
        >
          <Icon name={t.kind === 'error' ? 'info' : 'check-circle'} size={16} className="mt-0.5" />
          <span className="min-w-0 flex-1 break-words">{t.text}</span>
          <button type="button" onClick={() => dismiss(t.id)} aria-label="Fechar aviso" className="rounded-control p-0.5 opacity-70 hover:opacity-100 focus-visible:ring-2 focus-visible:ring-accent-primary">
            <Icon name="close" size={14} />
          </button>
        </div>
      ))}
    </div>
  )
}
