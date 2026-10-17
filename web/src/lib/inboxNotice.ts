import { useEffect, useRef, useState } from 'react'

const EVENT = 'omnira:inbox-notice'

interface NoticeDetail {
  conversationId: string
  text: string
}

/** Tells the open conversation something short happened ("Atendimento assumido por você."). Fire and forget. */
export function emitInboxNotice(conversationId: string, text: string): void {
  if (typeof window === 'undefined') return
  window.dispatchEvent(new CustomEvent<NoticeDetail>(EVENT, { detail: { conversationId, text } }))
}

// O aviso é só confirmação do que a pessoa acabou de fazer: some sozinho, não fica na tela até alguém fechar.
export const NOTICE_MS = 8000

/** The last notice for this conversation; cleared when another conversation is opened (it never leaks across attendances) and after NOTICE_MS. */
export function useInboxNotice(conversationId: string): [string, () => void] {
  const [text, setText] = useState('')
  const timer = useRef<number | undefined>(undefined)
  useEffect(() => {
    setText('')
    const on = (e: Event) => {
      const d = (e as CustomEvent<NoticeDetail>).detail
      if (d?.conversationId !== conversationId) return
      setText(d.text)
      window.clearTimeout(timer.current)
      timer.current = window.setTimeout(() => setText(''), NOTICE_MS)
    }
    window.addEventListener(EVENT, on)
    return () => {
      window.removeEventListener(EVENT, on)
      window.clearTimeout(timer.current)
    }
  }, [conversationId])
  return [text, () => setText('')]
}
