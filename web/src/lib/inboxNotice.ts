import { useEffect, useState } from 'react'

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

/** The last notice for this conversation; cleared when another conversation is opened (it never leaks across attendances). */
export function useInboxNotice(conversationId: string): [string, () => void] {
  const [text, setText] = useState('')
  useEffect(() => {
    setText('')
    const on = (e: Event) => {
      const d = (e as CustomEvent<NoticeDetail>).detail
      if (d?.conversationId === conversationId) setText(d.text)
    }
    window.addEventListener(EVENT, on)
    return () => window.removeEventListener(EVENT, on)
  }, [conversationId])
  return [text, () => setText('')]
}
