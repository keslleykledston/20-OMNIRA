import { describe, it, expect, vi, beforeEach } from 'vitest'
import { render, screen, fireEvent, waitFor } from '@testing-library/react'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { CredentialsConnectDialog } from '../features/channels/components/CredentialsConnectDialog'
import { integrationsAPI, type ProviderDescriptor } from '../lib/integrations'

const provider: ProviderDescriptor = {
  id: 'meta_cloud', name: 'WhatsApp oficial', channel: 'whatsapp', kind: 'official', connect_method: 'credentials',
  capabilities: [], enabled: true,
  inputs: [
    { key: 'phone_number_id', label: 'Phone Number ID', type: 'text', required: true, secret: false },
    { key: 'access_token', label: 'Token', type: 'secret', required: true, secret: true },
  ],
  displays: [],
}

function renderDialog() {
  const qc = new QueryClient()
  return render(
    <QueryClientProvider client={qc}>
      <CredentialsConnectDialog open provider={provider} onClose={() => {}} onChanged={() => {}} />
    </QueryClientProvider>,
  )
}

describe('CredentialsConnectDialog', () => {
  beforeEach(() => {
    vi.restoreAllMocks()
  })

  it('masks secrets, needs every required field, then shows callback and verify token without the secret', async () => {
    vi.spyOn(integrationsAPI, 'create').mockResolvedValue({
      id: 'c1', provider: 'meta_cloud', provider_kind: 'official', status: 'pending', capabilities: [], created_at: '',
      displays: { callback_url: 'https://x/webhooks/v1/whatsapp/meta', verify_token: 'omn-abc' },
    })
    renderDialog()
    const token = screen.getByLabelText('Token') as HTMLInputElement
    expect(token.type).toBe('password')
    const save = screen.getByRole('button', { name: 'Salvar conexão' })
    expect(save).toBeDisabled()
    fireEvent.change(screen.getByLabelText('Phone Number ID'), { target: { value: '123456789' } })
    fireEvent.change(token, { target: { value: 'EAAB-secret-token' } })
    fireEvent.click(save)
    await waitFor(() => expect(screen.getByText('omn-abc')).toBeInTheDocument())
    expect(screen.getByText('https://x/webhooks/v1/whatsapp/meta')).toBeInTheDocument()
    expect(document.body.textContent).not.toContain('EAAB-secret-token')
  })
})
