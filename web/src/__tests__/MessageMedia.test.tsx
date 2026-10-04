import { render, screen } from '@testing-library/react';
import { describe, it, expect } from 'vitest';
import MessageMedia from '../components/inbox/MessageMedia';
import type { MessageItem, MediaStatus } from '../types/api';

function msg(over: Partial<MessageItem>): MessageItem {
  return {
    id: '11111111-aaaa-4aaa-8aaa-000000000001',
    conversation_id: 'c',
    body: '',
    direction: 'inbound',
    status: 'received',
    created_at: '2026-10-04T12:00:00Z',
    mime_type: 'image/png',
    media_status: 'clean',
    ...over,
  };
}

const T = 'tenant-1';

describe('MessageMedia', () => {
  it('renders nothing for a message without media', () => {
    const { container } = render(<MessageMedia message={msg({ mime_type: undefined, media_status: undefined })} tenantId={T} />);
    expect(container).toBeEmptyDOMElement();
  });

  it('shows a cleared image through the authenticated endpoint only', () => {
    render(<MessageMedia message={msg({})} tenantId={T} />);
    const img = screen.getByRole('img');
    expect(img.getAttribute('src')).toBe(`/api/v1/tenants/${T}/messages/11111111-aaaa-4aaa-8aaa-000000000001/media`);
  });

  it('gives audio a native player and documents a download link', () => {
    const { container, rerender } = render(<MessageMedia message={msg({ mime_type: 'audio/ogg' })} tenantId={T} />);
    expect(container.querySelector('audio[controls]')).not.toBeNull();
    rerender(<MessageMedia message={msg({ mime_type: 'application/pdf', size_bytes: 2048 })} tenantId={T} />);
    const link = screen.getByRole('link');
    expect(link.getAttribute('href')).toContain('/media');
    expect(link.textContent).toContain('PDF');
    expect(link.textContent).toContain('2 KB');
  });

  const blocked: Array<[MediaStatus, RegExp, 'alert' | 'status']> = [
    ['pending', /Verificando/, 'status'],
    ['quarantined', /Verificando/, 'status'],
    ['infected', /antivírus detectou/, 'alert'],
    ['rejected', /tipo de arquivo não permitido/, 'alert'],
    ['failed', /não foi liberado/, 'alert'],
    ['source_gone', /indisponível/, 'status'],
  ];
  it.each(blocked)('%s never requests the file and explains why', (status, text, role) => {
    const { container } = render(<MessageMedia message={msg({ media_status: status })} tenantId={T} />);
    expect(screen.getByRole(role).textContent).toMatch(text);
    expect(container.querySelector('img, audio, video, a')).toBeNull();
  });
});
