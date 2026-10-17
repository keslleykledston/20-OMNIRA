/** Quanto falta para fechar a janela de 24 h do WhatsApp oficial (resposta livre). `urgent` quando restam menos de 2 h. */
export const WINDOW_URGENT_MS = 2 * 60 * 60 * 1000

export function windowLeft(expiresAt: string | Date, now: number = Date.now()): { text: string; urgent: boolean; closed: boolean } {
  const left = new Date(expiresAt).getTime() - now
  if (!Number.isFinite(left) || left <= 0) return { text: 'a janela fechou', urgent: true, closed: true }
  const minutes = Math.floor(left / 60_000)
  const text =
    minutes < 1 ? 'resta menos de 1 min'
    : minutes < 60 ? `restam ${minutes} min`
    : `restam ${Math.floor(minutes / 60)} h${minutes % 60 ? ` ${minutes % 60} min` : ''}`
  return { text, urgent: left < WINDOW_URGENT_MS, closed: false }
}
