import { describe, it, expect, beforeEach, vi } from 'vitest';
import { render, screen, waitFor } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import axios from 'axios';
import ConversationSummary from '../components/inbox/ConversationSummary';

vi.mock('axios');

const TENANT = '11111111-1111-1111-1111-111111111111';
const CONV_A = '22222222-2222-2222-2222-222222222222';
const CONV_B = '33333333-3333-3333-3333-333333333333';

describe('ConversationSummary', () => {
  beforeEach(() => {
    vi.resetAllMocks();
    localStorage.setItem('tenantId', TENANT);
    localStorage.setItem('jwtToken', 'tok');
  });

  it('never calls the summary endpoint on mount or on conversation change', () => {
    const { rerender } = render(<ConversationSummary conversationId={CONV_A} />);
    rerender(<ConversationSummary conversationId={CONV_B} />);
    expect(axios.post).not.toHaveBeenCalled();
  });

  it('generates a summary via POST with an empty body and shows it on success', async () => {
    vi.mocked(axios.post).mockResolvedValue({ status: 200, data: { summary: 'Cliente relatou problema resolvido.' } });
    render(<ConversationSummary conversationId={CONV_A} />);
    await userEvent.click(screen.getByText('Gerar resumo'));
    expect(await screen.findByText('Cliente relatou problema resolvido.')).toBeInTheDocument();

    const [url, body, config] = vi.mocked(axios.post).mock.calls[0] as any[];
    expect(url).toBe(`/api/v1/tenants/${TENANT}/conversations/${CONV_A}/ai/summary`);
    expect(body).toEqual({});
    expect(config.headers.Authorization).toBe('Bearer tok');
  });

  it('disables the button while the request is in flight', async () => {
    let resolvePost: (v: any) => void = () => {};
    vi.mocked(axios.post).mockReturnValue(new Promise((resolve) => { resolvePost = resolve; }) as any);
    render(<ConversationSummary conversationId={CONV_A} />);
    await userEvent.click(screen.getByText('Gerar resumo'));
    expect(screen.getByRole('button')).toBeDisabled();
    resolvePost({ status: 200, data: { summary: 'ok' } });
    await waitFor(() => expect(screen.getByRole('button')).not.toBeDisabled());
  });

  it('shows a rate-limited message on 429 without surfacing raw error text', async () => {
    vi.mocked(axios.post).mockRejectedValue({ response: { status: 429, data: 'too many summary requests, try again shortly' } });
    render(<ConversationSummary conversationId={CONV_A} />);
    await userEvent.click(screen.getByText('Gerar resumo'));
    expect(await screen.findByText('Muitas solicitações de resumo. Tente novamente em instantes.')).toBeInTheDocument();
  });

  it('shows an unavailable message on 503', async () => {
    vi.mocked(axios.post).mockRejectedValue({ response: { status: 503, data: 'ai summary is not available' } });
    render(<ConversationSummary conversationId={CONV_A} />);
    await userEvent.click(screen.getByText('Gerar resumo'));
    expect(await screen.findByText('Resumo por IA não está disponível no momento.')).toBeInTheDocument();
  });

  it('resets state when switching conversations, never showing the previous summary for the new one', async () => {
    vi.mocked(axios.post).mockResolvedValue({ status: 200, data: { summary: 'Resumo da conversa A.' } });
    const { rerender } = render(<ConversationSummary conversationId={CONV_A} />);
    await userEvent.click(screen.getByText('Gerar resumo'));
    expect(await screen.findByText('Resumo da conversa A.')).toBeInTheDocument();

    rerender(<ConversationSummary conversationId={CONV_B} />);
    expect(screen.queryByText('Resumo da conversa A.')).not.toBeInTheDocument();
    expect(screen.getByText('Gerar resumo')).toBeInTheDocument();
  });

  it('clears the session and never shows an error state on 401 (delegates to the shared session handler)', async () => {
    localStorage.setItem('sessionActive', 'true');
    vi.mocked(axios.post).mockRejectedValue({ response: { status: 401 } });
    render(<ConversationSummary conversationId={CONV_A} />);
    await userEvent.click(screen.getByText('Gerar resumo'));
    await waitFor(() => expect(localStorage.getItem('sessionActive')).toBeNull());
    expect(screen.queryByRole('alert')).not.toBeInTheDocument();
  });
});
