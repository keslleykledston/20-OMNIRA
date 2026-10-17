import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { act, render, renderHook, screen } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { ToastRegion } from '../components/primitives'
import { showToast, TOAST_MS } from '../lib/toast'
import { emitInboxNotice, NOTICE_MS, useInboxNotice } from '../lib/inboxNotice'

describe('toast', () => {
  beforeEach(() => { vi.useFakeTimers({ shouldAdvanceTime: true }) })
  afterEach(() => { vi.useRealTimers() })

  it('shows a success notice politely and removes it by itself', () => {
    render(<ToastRegion />)
    act(() => showToast('Detalhes salvos.'))
    expect(screen.getByRole('status')).toHaveTextContent('Detalhes salvos.')
    act(() => { vi.advanceTimersByTime(TOAST_MS + 10) })
    expect(screen.queryByText('Detalhes salvos.')).not.toBeInTheDocument()
  })

  it('keeps an error until it is closed, announced as an alert', async () => {
    const user = userEvent.setup({ advanceTimers: vi.advanceTimersByTime })
    render(<ToastRegion />)
    act(() => showToast('Não foi possível salvar.', 'error'))
    act(() => { vi.advanceTimersByTime(TOAST_MS * 5) })
    expect(screen.getByRole('alert')).toHaveTextContent('Não foi possível salvar.')
    await user.click(screen.getByRole('button', { name: 'Fechar aviso' }))
    expect(screen.queryByRole('alert')).not.toBeInTheDocument()
  })

  it('shows at most three at a time and ignores empty text', () => {
    render(<ToastRegion />)
    act(() => { for (const n of ['a1', 'b2', 'c3', 'd4']) showToast(n); showToast('   ') })
    expect(screen.queryByText('a1')).not.toBeInTheDocument()
    expect(screen.getAllByRole('status')).toHaveLength(3)
  })
})

describe('inbox notice', () => {
  beforeEach(() => { vi.useFakeTimers() })
  afterEach(() => { vi.useRealTimers() })

  it('goes away by itself, belongs to its own conversation, and a newer one restarts the clock', () => {
    const { result } = renderHook(() => useInboxNotice('c1'))
    act(() => emitInboxNotice('c2', 'de outra conversa'))
    expect(result.current[0]).toBe('')
    act(() => emitInboxNotice('c1', 'Atendimento assumido por você.'))
    expect(result.current[0]).toBe('Atendimento assumido por você.')
    act(() => { vi.advanceTimersByTime(NOTICE_MS - 1000) })
    act(() => emitInboxNotice('c1', 'Segundo aviso'))
    act(() => { vi.advanceTimersByTime(NOTICE_MS - 1000) })
    expect(result.current[0]).toBe('Segundo aviso')
    act(() => { vi.advanceTimersByTime(1500) })
    expect(result.current[0]).toBe('')
  })
})
