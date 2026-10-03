import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { act, fireEvent, screen, waitFor } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import axios from 'axios';
import ChatPane from '../components/inbox/ChatPane';
import { renderAt, setSession } from './testUtils';
import type { MessageItem } from '../types/api';

// The realtime hook is stubbed but its callback is kept, so a test can fire "something changed".
const realtime = vi.hoisted(() => ({ onEvent: undefined as undefined | (() => void) }));
vi.mock('axios');
vi.mock('../hooks/useRealtimeEvents', () => ({
  useRealtimeEvents: (opts: { onEvent?: () => void }) => {
    realtime.onEvent = opts.onEvent;
  },
}));

const CONV = 'conv-1';
const T = (h: number, m = 0) => new Date(2026, 9, 3, h, m).toISOString();
const msg = (id: string, h: number, over: Partial<MessageItem> = {}): MessageItem => ({
  id,
  conversation_id: CONV,
  body: `texto ${id}`,
  direction: 'inbound',
  status: 'received',
  created_at: T(h),
  ...over,
});

// Backend: newest-first pages of the whole thread, cursor = how many were already served.
let thread: MessageItem[] = [];
let pageSize = 100;
function serve() {
  vi.mocked(axios.get).mockImplementation(async (url: string, config?: any) => {
    if ((url as string).endsWith(`/inbox/conversations/${CONV}`)) {
      return { data: { id: CONV, contact_name: 'Maria', contact_phone: '+5511999990000', status: 'active' } };
    }
    if ((url as string).endsWith('/messages')) {
      const newestFirst = [...thread].sort((a, b) => b.created_at.localeCompare(a.created_at));
      const offset = Number(config?.params?.cursor || 0);
      const items = newestFirst.slice(offset, offset + pageSize);
      const more = offset + pageSize < newestFirst.length;
      return { data: { items, has_more: more, next_cursor: more ? String(offset + pageSize) : undefined } };
    }
    return Promise.reject({ response: { status: 404 } });
  });
}

const originals = {
  scrollHeight: Object.getOwnPropertyDescriptor(HTMLElement.prototype, 'scrollHeight'),
  clientHeight: Object.getOwnPropertyDescriptor(HTMLElement.prototype, 'clientHeight'),
};
beforeEach(() => {
  vi.resetAllMocks();
  localStorage.clear();
  setSession();
  pageSize = 100;
  Object.defineProperty(HTMLElement.prototype, 'scrollHeight', { configurable: true, get: () => 1000 });
  Object.defineProperty(HTMLElement.prototype, 'clientHeight', { configurable: true, get: () => 400 });
});
afterEach(() => {
  for (const [k, d] of Object.entries(originals)) {
    if (d) Object.defineProperty(HTMLElement.prototype, k, d);
    else delete (HTMLElement.prototype as any)[k];
  }
});

const scroller = () => document.querySelector('.overflow-y-auto') as HTMLElement;
const order = () => screen.getAllByText(/^texto /).map((n) => n.textContent);

describe('ChatPane — thread order and scrolling', () => {
  it('shows the oldest message at the top and the newest at the bottom', async () => {
    thread = [msg('c', 12), msg('a', 10), msg('b', 11)]; // API returns them newest first
    serve();
    renderAt(<ChatPane conversationId={CONV} />);
    await screen.findByText('texto a');
    expect(order()).toEqual(['texto a', 'texto b', 'texto c']);
  });

  it('anchors a short thread to the bottom, next to the composer', async () => {
    thread = [msg('a', 10)];
    serve();
    renderAt(<ChatPane conversationId={CONV} />);
    await screen.findByText('texto a');
    expect(document.querySelector('.justify-end')).not.toBeNull();
  });

  it('opens scrolled to the last message', async () => {
    thread = [msg('a', 10), msg('b', 11)];
    serve();
    renderAt(<ChatPane conversationId={CONV} />);
    await screen.findByText('texto b');
    expect(scroller().scrollTop).toBe(1000);
  });

  it('follows a message you send: back to the bottom even if you had scrolled up', async () => {
    const user = userEvent.setup();
    thread = [msg('a', 10), msg('b', 11)];
    serve();
    vi.mocked(axios.post).mockImplementation(async () => {
      thread = [...thread, msg('mine', 12, { direction: 'outbound', status: 'queued', body: 'texto mine' })];
      return { data: {} };
    });
    renderAt(<ChatPane conversationId={CONV} />);
    await screen.findByText('texto b');
    const el = scroller();
    el.scrollTop = 0; // reading old messages
    fireEvent.scroll(el);
    await user.type(screen.getByPlaceholderText('Escreva uma resposta...'), 'oi{Enter}');
    await screen.findByText('texto mine');
    await waitFor(() => expect(el.scrollTop).toBe(1000));
  });

  it('follows a received message while you are at the bottom, but does not yank you down while you read older ones', async () => {
    thread = [msg('a', 10), msg('b', 11)];
    serve();
    renderAt(<ChatPane conversationId={CONV} />);
    await screen.findByText('texto b');
    const el = scroller();
    expect(el.scrollTop).toBe(1000);

    // at the bottom: a received message is followed
    el.scrollTop = 900; // 1000 - 900 - 400 < 160: still "at the bottom"
    fireEvent.scroll(el);
    thread = [...thread, msg('c', 12)];
    await act(async () => realtime.onEvent?.());
    await screen.findByText('texto c');
    await waitFor(() => expect(el.scrollTop).toBe(1000));

    // reading older messages: a new one arrives, the view stays and the jump button shows
    el.scrollTop = 100; // 800 above the bottom
    fireEvent.scroll(el);
    thread = [...thread, msg('d', 13)];
    await act(async () => realtime.onEvent?.());
    await screen.findByText('texto d');
    expect(el.scrollTop).toBe(100);
    expect(screen.getByLabelText('Ir para a última mensagem')).toBeInTheDocument();
  });

  it('the jump button goes to the last message and then disappears', async () => {
    const user = userEvent.setup();
    thread = [msg('a', 10), msg('b', 11)];
    serve();
    renderAt(<ChatPane conversationId={CONV} />);
    await screen.findByText('texto b');
    const el = scroller();
    el.scrollTop = 0;
    fireEvent.scroll(el);
    await user.click(await screen.findByLabelText('Ir para a última mensagem'));
    expect(el.scrollTop).toBe(1000);
    expect(screen.queryByLabelText('Ir para a última mensagem')).not.toBeInTheDocument();
  });
});

describe('ChatPane — older messages', () => {
  it('loads older pages on scroll to the top and keeps them above, in order, without duplicates', async () => {
    pageSize = 2;
    thread = [msg('1', 8), msg('2', 9), msg('3', 10), msg('4', 11), msg('5', 12)];
    serve();
    renderAt(<ChatPane conversationId={CONV} />);
    await screen.findByText('texto 5');
    expect(order()).toEqual(['texto 4', 'texto 5']);
    const el = scroller();
    el.scrollTop = 0;
    fireEvent.scroll(el);
    await screen.findByText('texto 3');
    await waitFor(() => expect(order()).toEqual(['texto 2', 'texto 3', 'texto 4', 'texto 5']));
    fireEvent.scroll(el);
    await screen.findByText('texto 1');
    expect(order()).toEqual(['texto 1', 'texto 2', 'texto 3', 'texto 4', 'texto 5']);
    expect(screen.queryByText('Carregar mensagens anteriores')).not.toBeInTheDocument();
  });

  it('offers a button to load older messages while there are more', async () => {
    const user = userEvent.setup();
    pageSize = 2;
    thread = [msg('1', 8), msg('2', 9), msg('3', 10)];
    serve();
    renderAt(<ChatPane conversationId={CONV} />);
    await user.click(await screen.findByText('Carregar mensagens anteriores'));
    await screen.findByText('texto 1');
  });
});

describe('ChatPane — day separators', () => {
  it('uses "Hoje"/"Ontem" and does not repeat a day within the same day', async () => {
    const now = new Date();
    const yesterday = new Date(now.getFullYear(), now.getMonth(), now.getDate() - 1, 10).toISOString();
    const today1 = new Date(now.getFullYear(), now.getMonth(), now.getDate(), 0, 5).toISOString();
    const today2 = new Date(now.getFullYear(), now.getMonth(), now.getDate(), 0, 6).toISOString();
    thread = [
      msg('y', 10, { created_at: yesterday }),
      msg('t1', 10, { created_at: today1 }),
      msg('t2', 10, { created_at: today2 }),
    ];
    serve();
    renderAt(<ChatPane conversationId={CONV} />);
    await screen.findByText('texto t2');
    expect(screen.getAllByText('Hoje')).toHaveLength(1);
    expect(screen.getAllByText('Ontem')).toHaveLength(1);
  });
});
