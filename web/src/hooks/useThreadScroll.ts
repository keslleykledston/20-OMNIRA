import { useCallback, useEffect, useLayoutEffect, useRef, useState } from 'react'

interface Options<T extends { id: string }> {
  /** Identifies the thread (conversation or group); changing it reopens the thread at its end. */
  threadKey: string
  /** Oldest first. */
  messages: T[]
  /** True for a message that must always scroll into view (e.g. one the user just sent). */
  alwaysFollow?: (last: T) => boolean
  hasMore: boolean
  isFetchingMore: boolean
  fetchMore: () => unknown
}

/**
 * Chat-style scrolling. A thread opens on its newest message and follows new ones while the reader
 * is at the bottom; if they scrolled up to read, it stays put and a "jump to last" button is
 * offered. Loading an older page keeps the view where it was instead of jumping. Attach `scrollRef`
 * to the element that scrolls (it MUST be height-bounded, e.g. `absolute inset-0`), `contentRef` to
 * its inner wrapper and `onScroll` to the scroller.
 */
export function useThreadScroll<T extends { id: string }>({ threadKey, messages, alwaysFollow, hasMore, isFetchingMore, fetchMore }: Options<T>) {
  const scrollRef = useRef<HTMLDivElement>(null)
  const contentRef = useRef<HTMLDivElement>(null)
  const stickRef = useRef(true)
  const prependRef = useRef<{ height: number; top: number } | null>(null)
  const lastIdRef = useRef<string | null>(null)
  const openedRef = useRef<string | null>(null)
  const [atBottom, setAtBottom] = useState(true)

  const scrollToBottom = useCallback(() => {
    const el = scrollRef.current
    if (el) el.scrollTop = el.scrollHeight
  }, [])

  useLayoutEffect(() => {
    const el = scrollRef.current
    if (!el || messages.length === 0) return
    const last = messages[messages.length - 1]
    if (openedRef.current !== threadKey) {
      openedRef.current = threadKey
      lastIdRef.current = last.id
      stickRef.current = true
      scrollToBottom()
      return
    }
    if (prependRef.current) {
      el.scrollTop = el.scrollHeight - prependRef.current.height + prependRef.current.top
      prependRef.current = null
      return
    }
    if (last.id !== lastIdRef.current) {
      lastIdRef.current = last.id
      if (stickRef.current || alwaysFollow?.(last)) {
        stickRef.current = true
        scrollToBottom()
      }
    }
  }, [messages, threadKey, alwaysFollow, scrollToBottom])

  // Images/audio finish loading after the first paint and grow the thread: keep following the end.
  useEffect(() => {
    const content = contentRef.current
    if (!content || typeof ResizeObserver === 'undefined') return
    const observer = new ResizeObserver(() => {
      if (stickRef.current) scrollToBottom()
    })
    observer.observe(content)
    return () => observer.disconnect()
  }, [threadKey, scrollToBottom])

  const requestOlder = useCallback(() => {
    const el = scrollRef.current
    if (!el || !hasMore || isFetchingMore) return
    prependRef.current = { height: el.scrollHeight, top: el.scrollTop }
    void fetchMore()
  }, [hasMore, isFetchingMore, fetchMore])

  const onScroll = useCallback(() => {
    const el = scrollRef.current
    if (!el) return
    const nearBottom = el.scrollHeight - el.scrollTop - el.clientHeight < 160
    stickRef.current = nearBottom
    setAtBottom(nearBottom)
    if (el.scrollTop < 80) requestOlder()
  }, [requestOlder])

  const jumpToBottom = useCallback(() => {
    stickRef.current = true
    scrollToBottom()
    setAtBottom(true)
  }, [scrollToBottom])

  return { scrollRef, contentRef, atBottom, onScroll, requestOlder, jumpToBottom }
}
