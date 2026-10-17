export type ToastKind = 'success' | 'error' | 'info'

export interface ToastDetail {
  id: number
  text: string
  kind: ToastKind
}

export const TOAST_EVENT = 'omnira:toast'
// Sucesso e informação somem sozinhos; erro fica até a pessoa fechar (ela precisa ter tempo de ler e agir).
export const TOAST_MS = 5000

let seq = 0

/** Um aviso curto e passageiro ("Alterações salvas."). Dispara e esquece; a região de avisos do app mostra. */
export function showToast(text: string, kind: ToastKind = 'success'): void {
  if (typeof window === 'undefined' || !text.trim()) return
  window.dispatchEvent(new CustomEvent<ToastDetail>(TOAST_EVENT, { detail: { id: ++seq, text, kind } }))
}
