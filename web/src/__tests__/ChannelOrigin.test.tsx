import { describe, expect, it } from 'vitest';
import { render, screen } from '@testing-library/react';
import { ChannelOrigin, channelKind } from '../components/inbox/ChannelOrigin';

describe('channelKind', () => {
  it.each([
    ['waha', 'whatsapp'], ['meta_cloud', 'whatsapp'], ['WhatsApp', 'whatsapp'], ['whatsapp oficial', 'whatsapp'],
    ['email', 'email'], ['SMTP', 'email'], ['instagram', 'instagram'], ['Instagram DM', 'instagram'],
    ['facebook', 'facebook'], ['messenger', 'facebook'], ['', 'unknown'], [undefined, 'unknown'], ['telegrama', 'unknown'],
  ] as const)('%s -> %s', (src, kind) => expect(channelKind(src)).toBe(kind));
});

describe('ChannelOrigin', () => {
  it('names its origin in text, never by the logo alone, and adds the line label to the tooltip', () => {
    render(<ChannelOrigin source="waha" detail="Linha principal" />);
    const el = screen.getByRole('img', { name: 'WhatsApp' });
    expect(el).toHaveAttribute('title', 'WhatsApp · Linha principal');
  });
  it('an unknown or empty source says so instead of guessing', () => {
    render(<ChannelOrigin source="" />);
    expect(screen.getByRole('img', { name: 'Canal não informado' })).toBeInTheDocument();
  });
  it('each future channel already has its own name', () => {
    render(<><ChannelOrigin source="email" /><ChannelOrigin source="instagram" /><ChannelOrigin source="facebook" /></>);
    for (const n of ['E-mail', 'Instagram', 'Facebook']) expect(screen.getByRole('img', { name: n })).toBeInTheDocument();
  });
});

describe('channelLabel', () => {
  it('reads like a name, keeps an unknown provider as written, and says so when there is none', async () => {
    const { channelLabel } = await import('../components/inbox/ChannelOrigin');
    expect(channelLabel('waha')).toBe('WhatsApp');
    expect(channelLabel('telegrama')).toBe('telegrama');
    expect(channelLabel('')).toBe('Canal não informado');
  });
});
