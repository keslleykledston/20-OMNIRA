import { describe, it, expect, vi, beforeEach } from 'vitest'
import { render, screen, fireEvent, waitFor } from '@testing-library/react'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import axios from 'axios'
import { TemplateSendDialog } from '../components/inbox/TemplateSendDialog'

vi.mock('axios')
const mocked = vi.mocked(axios, true)

const items = [
  { id: 't1', name: 'boas_vindas', language: 'pt_BR', category: 'UTILITY', status: 'APPROVED', body: 'Olá {{1}}, seu chamado {{2}} foi aberto.', variable_count: 2, sendable: true },
  { id: 't2', name: 'com_imagem', language: 'pt_BR', category: 'MARKETING', status: 'APPROVED', body: 'Promo', variable_count: 0, sendable: false, unsupported_reason: 'cabeçalho com mídia (image)' },
]

function renderDialog(onClose = () => {}) {
  return render(
    <QueryClientProvider client={new QueryClient()}>
      <TemplateSendDialog open conversationId="conv-1" connectionId="line-1" contactName="Maria" onClose={onClose} />
    </QueryClientProvider>,
  )
}

describe('TemplateSendDialog', () => {
  beforeEach(() => {
    vi.resetAllMocks()
    localStorage.setItem('tenantId', 't-1')
    mocked.get.mockResolvedValue({ data: { items } })
  })

  it('previews the filled template, validates the variables and sends with an idempotency key', async () => {
    mocked.post.mockResolvedValue({ data: { id: 'm1' } })
    const onClose = vi.fn()
    renderDialog(onClose)
    fireEvent.change(await screen.findByLabelText('Template'), { target: { value: 't1' } })
    const send = screen.getByRole('button', { name: 'Enviar template' })
    expect(send).toBeDisabled()
    fireEvent.change(screen.getByLabelText('Variável {{1}}'), { target: { value: 'Ana' } })
    fireEvent.change(screen.getByLabelText('Variável {{2}}'), { target: { value: '123' } })
    expect(screen.getByTestId('template-preview')).toHaveTextContent('Olá Ana, seu chamado 123 foi aberto.')
    fireEvent.click(send)
    await waitFor(() => expect(onClose).toHaveBeenCalled())
    const [url, body, cfg] = mocked.post.mock.calls[0] as [string, unknown, { headers: Record<string, string> }]
    expect(url).toMatch(/inbox\/conversations\/conv-1\/template$/)
    expect(body).toEqual({ template_id: 't1', params: ['Ana', '123'] })
    expect(cfg.headers['Idempotency-Key']).toBeTruthy()
  })

  it('lists what cannot be sent with the reason instead of hiding it, and refuses a tab in a variable', async () => {
    renderDialog()
    expect(await screen.findByText(/não dá para enviar por aqui/)).toBeInTheDocument()
    fireEvent.change(await screen.findByLabelText('Template'), { target: { value: 't1' } })
    fireEvent.change(screen.getByLabelText('Variável {{1}}'), { target: { value: 'Ana\tSilva' } })
    fireEvent.change(screen.getByLabelText('Variável {{2}}'), { target: { value: '1' } })
    expect(screen.getByRole('button', { name: 'Enviar template' })).toBeDisabled()
  })

  it('tells the attendant what to do when the line has no approved template', async () => {
    mocked.get.mockResolvedValue({ data: { items: [] } })
    renderDialog()
    expect(await screen.findByText(/Nenhum template aprovado disponível/)).toBeInTheDocument()
  })
})
