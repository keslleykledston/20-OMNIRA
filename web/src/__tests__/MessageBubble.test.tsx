import { describe, it, expect } from 'vitest';
import { render, screen } from '@testing-library/react';
import MessageBubble from '../components/inbox/MessageBubble';
import { MessageItem } from '../types/api';

// PILOT.4A2: 'uncertain' must be visually and textually distinct from
// 'failed' — OMNIRA cannot prove the provider rejected the message, so it
// must never say "failed" for this status.
function outboundMessage(status: MessageItem['status']): MessageItem {
  return {
    id: 'msg-1',
    conversation_id: 'conv-1',
    body: 'oi',
    direction: 'outbound',
    status,
    created_at: new Date().toISOString(),
  };
}

describe('MessageBubble', () => {
  it('shows a distinct affordance for uncertain, not the failed treatment', () => {
    render(<MessageBubble message={outboundMessage('uncertain')} />);
    const status = screen.getByTitle('A entrega não pôde ser confirmada');
    expect(status).toBeInTheDocument();
    expect(status.textContent).toBe('?');
    expect(status.textContent).not.toBe('⚠');
    expect(status.className).not.toContain('text-status-danger');
  });

  it('still renders failed with its own distinct treatment', () => {
    render(<MessageBubble message={outboundMessage('failed')} />);
    const bubble = document.querySelector('.text-status-danger');
    expect(bubble).not.toBeNull();
    expect(bubble?.textContent).toBe('⚠');
    expect(bubble?.getAttribute('title')).toBeFalsy();
  });

  it('still renders sent unaffected', () => {
    render(<MessageBubble message={outboundMessage('sent')} />);
    expect(screen.getByText('✓')).toBeInTheDocument();
  });
});

describe('MessageBubble — failed delivery', () => {
  it('says why a message was not delivered when the provider gave a reason', () => {
    render(<MessageBubble message={{ id: 'm', conversation_id: 'c', direction: 'outbound', status: 'failed', body: 'oi', created_at: '2026-10-07T06:43:00Z', failure_reason: 'provider:131026: Message undeliverable' }} />)
    expect(screen.getByRole('status')).toHaveTextContent('Não foi entregue (131026: Message undeliverable).')
  })
})
