import { describe, it, expect, vi } from 'vitest'
import { render, screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import MessageComposer from '../components/inbox/MessageComposer'
import { describeAttachmentError, uploadAttachment, MAX_ATTACHMENT_BYTES } from '../lib/attachments'
import type { UploadedAttachment } from '../lib/attachments'

const uploaded: UploadedAttachment = { id: 'att-1', kind: 'document', mime: 'application/pdf', size_bytes: 2048, file_name: 'boleto.pdf', expires_at: '2099-01-01T00:00:00Z' }
const pdf = () => new File(['%PDF-1.4'], 'boleto.pdf', { type: 'application/pdf' })

describe('outbound attachments in the composer (ADR-0024)', () => {
  it('shows no paperclip when the server or the line cannot send files', () => {
    render(<MessageComposer draftKey="t:a" onSend={vi.fn()} />)
    expect(screen.queryByLabelText('Anexar arquivo')).toBeNull()
  })

  it('uploads, shows the file, and sends it without any text', async () => {
    const onAttach = vi.fn().mockResolvedValue(uploaded)
    const onSend = vi.fn().mockResolvedValue(true)
    render(<MessageComposer draftKey="t:b" onSend={onSend} onAttach={onAttach} />)
    await userEvent.upload(screen.getByTestId('attachment-input'), pdf())
    expect(await screen.findByText('boleto.pdf')).toBeInTheDocument()
    expect(screen.getByText('2 KB')).toBeInTheDocument()
    const send = screen.getByLabelText('Enviar mensagem')
    expect(send).toBeEnabled()
    await userEvent.click(send)
    expect(onSend).toHaveBeenCalledWith('', 'att-1')
    await waitFor(() => expect(screen.queryByTestId('attachment-chip')).toBeNull())
  })

  it('sends the typed text as the caption and keeps the file when the send fails', async () => {
    const onSend = vi.fn().mockResolvedValueOnce(false).mockResolvedValueOnce(true)
    render(<MessageComposer draftKey="t:c" onSend={onSend} onAttach={vi.fn().mockResolvedValue(uploaded)} />)
    await userEvent.upload(screen.getByTestId('attachment-input'), pdf())
    await screen.findByText('boleto.pdf')
    await userEvent.type(screen.getByPlaceholderText('Legenda (opcional)...'), 'Segue o boleto')
    await userEvent.click(screen.getByLabelText('Enviar mensagem'))
    expect(onSend).toHaveBeenLastCalledWith('Segue o boleto', 'att-1')
    expect(screen.getByTestId('attachment-chip')).toBeInTheDocument() // a failed send must not lose the file
    await userEvent.click(screen.getByLabelText('Enviar mensagem'))
    await waitFor(() => expect(screen.queryByTestId('attachment-chip')).toBeNull())
  })

  it('says why a file was refused and attaches nothing', async () => {
    const err = { response: { status: 422, data: 'file not accepted: archive_not_allowed' } }
    render(<MessageComposer draftKey="t:d" onSend={vi.fn()} onAttach={vi.fn().mockRejectedValue(err)} describeError={describeAttachmentError} />)
    await userEvent.upload(screen.getByTestId('attachment-input'), pdf())
    expect(await screen.findByRole('alert')).toHaveTextContent('Arquivos compactados não podem ser enviados.')
    expect(screen.queryByTestId('attachment-chip')).toBeNull()
    expect(screen.getByLabelText('Enviar mensagem')).toBeDisabled()
  })

  it('lets the operator take the file out, telling the server', async () => {
    const onRemove = vi.fn()
    render(<MessageComposer draftKey="t:e" onSend={vi.fn()} onAttach={vi.fn().mockResolvedValue(uploaded)} onRemoveAttachment={onRemove} />)
    await userEvent.upload(screen.getByTestId('attachment-input'), pdf())
    await screen.findByText('boleto.pdf')
    await userEvent.click(screen.getByLabelText('Remover anexo'))
    expect(onRemove).toHaveBeenCalledWith('att-1')
    expect(screen.queryByTestId('attachment-chip')).toBeNull()
  })

  it('keeps the chosen file per conversation when switching away and back', async () => {
    const onAttach = vi.fn().mockResolvedValue(uploaded)
    const { rerender } = render(<MessageComposer draftKey="t:f1" onSend={vi.fn()} onAttach={onAttach} />)
    await userEvent.upload(screen.getByTestId('attachment-input'), pdf())
    await screen.findByText('boleto.pdf')
    rerender(<MessageComposer draftKey="t:f2" onSend={vi.fn()} onAttach={onAttach} />)
    expect(screen.queryByTestId('attachment-chip')).toBeNull() // another attendance must never carry the file over
    rerender(<MessageComposer draftKey="t:f1" onSend={vi.fn()} onAttach={onAttach} />)
    expect(await screen.findByText('boleto.pdf')).toBeInTheDocument()
  })

  it('does not allow a second file on the same message', async () => {
    render(<MessageComposer draftKey="t:g" onSend={vi.fn()} onAttach={vi.fn().mockResolvedValue(uploaded)} />)
    await userEvent.upload(screen.getByTestId('attachment-input'), pdf())
    await screen.findByText('boleto.pdf')
    expect(screen.getByLabelText('Anexar arquivo')).toBeDisabled()
  })
})

describe('attachment helpers', () => {
  it('turns every server answer into a sentence', () => {
    const e = (status: number, data = '') => describeAttachmentError({ response: { status, data } })
    expect(e(413)).toMatch(/16 MB/)
    expect(e(422, 'file not accepted: pdf_active_content')).toMatch(/scripts/)
    expect(e(422, 'file not accepted: unsupported_for_channel')).toMatch(/canal/)
    expect(e(422, 'the file was blocked by the antivirus')).toMatch(/antivírus/)
    expect(e(422, 'attachment is not available (expired, already sent or not yours)')).toMatch(/expirou/)
    expect(e(503)).toMatch(/antivírus está indisponível/)
    expect(e(409, 'too many unsent attachments in this conversation')).toMatch(/demais/)
    expect(e(409, 'customer service window closed')).toMatch(/Janela/)
    expect(e(501)).toMatch(/não está habilitado/)
    expect(e(500)).toBeTruthy()
  })

  it('refuses a file over the limit without uploading it', async () => {
    const big = new File([new Uint8Array(1)], 'grande.pdf')
    Object.defineProperty(big, 'size', { value: MAX_ATTACHMENT_BYTES + 1 })
    await expect(uploadAttachment('t', 'c', big)).rejects.toMatchObject({ response: { status: 413 } })
  })
})
