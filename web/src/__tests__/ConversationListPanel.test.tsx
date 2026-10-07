import { describe, expect, it, vi } from 'vitest';
import { fireEvent, render, screen } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import ConversationListPanel from '../components/inbox/ConversationListPanel';
import type { ConversationItem } from '../types/api';

const minutesAgo = (m: number) => new Date(Date.now() - m * 60_000).toISOString();

function conv(id: string, over: Partial<ConversationItem> = {}): ConversationItem {
  return {
    id,
    contact_name: `Contato ${id}`,
    contact_phone: '+5592999990000',
    status: 'active',
    updated_at: minutesAgo(5),
    assigned_to_user_id: 'u1',
    ...over,
  } as ConversationItem;
}

function renderPanel(items: ConversationItem[], over: Partial<React.ComponentProps<typeof ConversationListPanel>> = {}) {
  const props = {
    conversations: items,
    selectedId: null,
    onSelect: vi.fn(),
    segment: 'all' as const,
    onSegmentChange: vi.fn(),
    search: '',
    onSearchChange: vi.fn(),
    isLoading: false,
    ...over,
  };
  render(<ConversationListPanel {...props} />);
  return props;
}

describe('ConversationListPanel — compact rows', () => {
  it('keeps the order it is given (the API sends the most recent activity first)', () => {
    renderPanel([conv('novo'), conv('velho')]);
    const rows = screen.getAllByRole('listitem');
    expect(rows[0]).toHaveTextContent('Contato novo');
    expect(rows[1]).toHaveTextContent('Contato velho');
  });

  it('shows the real last message and who wrote it, not a placeholder', () => {
    renderPanel([
      conv('a', { last_message_at: minutesAgo(2), last_message_preview: 'preciso de ajuda', last_message_direction: 'inbound' }),
      conv('b', { last_message_at: minutesAgo(3), last_message_preview: 'combinado', last_message_direction: 'outbound' }),
    ]);
    expect(screen.getByText('preciso de ajuda')).toBeInTheDocument();
    expect(screen.getByText('Você: combinado')).toBeInTheDocument();
    expect(screen.queryByText('Sem preview recente')).not.toBeInTheDocument();
  });

  it('shows the wait only for conversations where the customer is waiting, never a blanket "Pendente"', () => {
    renderPanel([
      conv('esperando', { last_message_at: minutesAgo(50), last_message_direction: 'inbound', waiting_since: minutesAgo(50) }),
      conv('respondida', { last_message_at: minutesAgo(10), last_message_direction: 'outbound' }),
    ]);
    const [waiting, answered] = screen.getAllByRole('listitem');
    expect(waiting).toHaveTextContent('50 min');
    expect(answered).not.toHaveTextContent('min');
    expect(screen.queryByText('Pendente')).not.toBeInTheDocument();
  });

  it('colours the wait with the tenant thresholds: the same 50 minutes is critical for a strict tenant', () => {
    const row = conv('x', { last_message_at: minutesAgo(50), last_message_direction: 'inbound', waiting_since: minutesAgo(50) });
    const { unmount } = render(
      <ConversationListPanel conversations={[row]} selectedId={null} onSelect={vi.fn()} segment="all" onSegmentChange={vi.fn()} search="" onSearchChange={vi.fn()} isLoading={false} />
    );
    expect(screen.getByTitle('Cliente aguardando resposta').className).toContain('status-warning'); // standard 30 min / 2 h
    unmount();
    render(
      <ConversationListPanel conversations={[row]} selectedId={null} onSelect={vi.fn()} segment="all" onSegmentChange={vi.fn()} search="" onSearchChange={vi.fn()} isLoading={false} waitThresholds={{ warnMinutes: 10, dangerMinutes: 40 }} />
    );
    expect(screen.getByTitle('Cliente aguardando resposta').className).toContain('status-danger');
  });

  it('flags a conversation with no attendant and leaves assigned ones clean', () => {
    renderPanel([conv('livre', { assigned_to_user_id: undefined }), conv('minha')]);
    expect(screen.getAllByLabelText('Sem atendente')).toHaveLength(1);
  });

  it('marks the selected row and reports clicks', async () => {
    const props = renderPanel([conv('a'), conv('b')], { selectedId: 'b' });
    expect(screen.getByRole('button', { name: /Contato b/ })).toHaveAttribute('aria-current', 'true');
    await userEvent.click(screen.getByRole('button', { name: /Contato a/ }));
    expect(props.onSelect).toHaveBeenCalledWith('a');
  });

  it('says what is missing: no results for a search vs. an empty filter', () => {
    const { unmount } = render(
      <ConversationListPanel conversations={[]} selectedId={null} onSelect={vi.fn()} segment="all" onSegmentChange={vi.fn()} search="zzz" onSearchChange={vi.fn()} isLoading={false} />
    );
    expect(screen.getByText('Nada encontrado para esta busca.')).toBeInTheDocument();
    unmount();
    renderPanel([], { segment: 'waiting' });
    expect(screen.getByText('Quando um cliente escrever, a conversa aparece aqui.')).toBeInTheDocument();
  });

  it('reports typing in the search box and tab changes', async () => {
    const props = renderPanel([conv('a')]);
    await userEvent.type(screen.getByRole('searchbox'), 'x');
    expect(props.onSearchChange).toHaveBeenCalledWith('x');
    await userEvent.click(screen.getByRole('tab', { name: 'Aguardando' }));
    expect(props.onSegmentChange).toHaveBeenCalledWith('waiting');
  });
});

describe('ConversationListPanel — no cap on the list', () => {
  function stubScroll(el: HTMLElement, m: { scrollHeight: number; clientHeight: number }) {
    Object.defineProperty(el, 'scrollHeight', { configurable: true, value: m.scrollHeight });
    Object.defineProperty(el, 'clientHeight', { configurable: true, value: m.clientHeight });
  }

  it('asks for the next page when scrolled near the bottom, not before', () => {
    const onLoadMore = vi.fn();
    renderPanel([conv('a')], { hasMore: true, onLoadMore });
    const scroller = screen.getByRole('list').parentElement as HTMLElement;
    stubScroll(scroller, { scrollHeight: 5000, clientHeight: 600 });
    onLoadMore.mockClear();
    scroller.scrollTop = 100;
    fireEvent.scroll(scroller);
    expect(onLoadMore).not.toHaveBeenCalled();
    scroller.scrollTop = 4300; // 5000 - 4300 - 600 = 100 < 240
    fireEvent.scroll(scroller);
    expect(onLoadMore).toHaveBeenCalledTimes(1);
  });

  it('does not ask again while a page is loading, nor when there is nothing more', () => {
    const onLoadMore = vi.fn();
    renderPanel([conv('a')], { hasMore: true, isFetchingMore: true, onLoadMore });
    expect(onLoadMore).not.toHaveBeenCalled();
    expect(screen.getByText('Carregando mais...')).toBeInTheDocument();
  });

  it('keeps loading when the rows do not even fill the panel', () => {
    const onLoadMore = vi.fn();
    renderPanel([conv('a')], { hasMore: true, onLoadMore });
    // jsdom has zero heights: content (0) never exceeds the panel (0) by 240px, so it asks
    expect(onLoadMore).toHaveBeenCalled();
  });
});

describe('ConversationListPanel — Spam inbox', () => {
  it('offers the scope (in progress / finalized), the three primary filters and the rest under "Mais…", Spam last', () => {
    renderPanel([conv('a')]);
    expect(screen.getAllByRole('tab').map((t) => t.textContent)).toEqual(['Em andamento', 'Encerradas', 'Todas', 'Aguardando', 'Minhas']);
    const more = screen.getByLabelText('Mais filtros') as HTMLSelectElement;
    expect(Array.from(more.options).map((o) => o.textContent)).toEqual(['Mais…', 'Não classif.', 'Internas', 'Spam']);
  });

  it('in Spam nobody is "waiting" and nothing is "unassigned": those signals are for conversations to attend', () => {
    renderPanel(
      [conv('golpe', { assigned_to_user_id: undefined, last_message_at: minutesAgo(90), last_message_direction: 'inbound', waiting_since: minutesAgo(90) })],
      { segment: 'spam' }
    );
    const row = screen.getByRole('listitem');
    expect(row).not.toHaveTextContent('min');
    expect(screen.queryByLabelText('Sem atendente')).not.toBeInTheDocument();
  });

  it('outside Spam the same conversation still shows its wait', () => {
    renderPanel([conv('x', { assigned_to_user_id: undefined, last_message_at: minutesAgo(90), last_message_direction: 'inbound', waiting_since: minutesAgo(90) })]);
    expect(screen.getByRole('listitem')).toHaveTextContent('1 h');
    expect(screen.getByLabelText('Sem atendente')).toBeInTheDocument();
  });

  it('explains how to restore when the Spam inbox is empty', () => {
    renderPanel([], { segment: 'spam' });
    expect(screen.getByText(/Contatos marcados como spam/)).toBeInTheDocument();
    expect(screen.getByText(/Não é spam/)).toBeInTheDocument();
  });
});


describe('ConversationListPanel — what a conversation is (ADR-0018)', () => {
  it('badges unclassified, internal and other conversations and nothing for plain customer service', () => {
    renderPanel([
      conv('cliente', { conversation_kind: 'customer_service' }),
      conv('novo', { conversation_kind: 'unclassified', has_unclassified_participants: true }),
      conv('time', { conversation_kind: 'internal' }),
      conv('forn', { conversation_kind: 'external_other' }),
      conv('sem-campo'),
    ]);
    const rows = screen.getAllByRole('listitem');
    const row = (name: string) => rows.find((r) => r.textContent?.includes(name))!;
    expect(row('Contato novo')).toHaveTextContent('Não classificado');
    expect(row('Contato time')).toHaveTextContent('Interna');
    expect(row('Contato forn')).toHaveTextContent('Outros');
    for (const name of ['Contato cliente', 'Contato sem-campo']) {
      expect(row(name)).not.toHaveTextContent(/Não classificado|Interna|Outros/);
    }
  });

  it('a conversation with staff has no "waiting" signal and nothing to claim', () => {
    renderPanel([
      conv('time', { conversation_kind: 'internal', assigned_to_user_id: undefined, last_message_at: minutesAgo(90), last_message_direction: 'inbound', waiting_since: minutesAgo(90) }),
    ], { segment: 'internal' });
    const row = screen.getByRole('listitem');
    expect(row).not.toHaveTextContent('min');
    expect(screen.queryByLabelText('Sem atendente')).not.toBeInTheDocument();
  });

  it('switching to the new filters reports the segment, and each has its own empty state', async () => {
    const user = userEvent.setup();
    const props = renderPanel([], { segment: 'unclassified' });
    expect(screen.getByText(/Quando alguém novo escrever/)).toBeInTheDocument();
    await user.selectOptions(screen.getByLabelText('Mais filtros'), 'internal');
    expect(props.onSegmentChange).toHaveBeenCalledWith('internal');
  });
});


describe('ConversationListPanel — alias first, WhatsApp name below', () => {
  it('shows the principal name and, smaller below it, the name declared on WhatsApp when they differ', () => {
    renderPanel([
      conv('a', { contact_name: 'José Carlos (ACME)', contact_whatsapp_name: 'Zé Boladão' }),
      conv('b', { contact_name: 'Maria', contact_whatsapp_name: 'Maria' }),
      conv('c', { contact_name: 'Pedro' }),
    ]);
    const rows = screen.getAllByRole('listitem');
    expect(rows[0]).toHaveTextContent('José Carlos (ACME)');
    expect(rows[0]).toHaveTextContent('WhatsApp: Zé Boladão');
    expect(rows[1]).not.toHaveTextContent('WhatsApp:');
    expect(rows[2]).not.toHaveTextContent('WhatsApp:');
  });
});


describe('ConversationListPanel — declared internal contacts', () => {
  it('says which kind of internal contact it is instead of "Outros", and keeps the wait signal (somebody must answer)', () => {
    renderPanel([
      conv('forn', { conversation_kind: 'external_other', contact_kind: 'internal', contact_internal_role: 'supplier', last_message_at: minutesAgo(40), last_message_direction: 'inbound', waiting_since: minutesAgo(40) }),
      conv('eq', { conversation_kind: 'external_other', contact_kind: 'internal', contact_internal_role: 'team' }),
      conv('outro', { conversation_kind: 'external_other', contact_kind: 'other' }),
    ])
    const rows = screen.getAllByRole('listitem')
    expect(rows[0]).toHaveTextContent('Fornecedor')
    expect(rows[0]).not.toHaveTextContent('Outros')
    expect(rows[0]).toHaveTextContent('40 min')
    expect(rows[1]).toHaveTextContent('Equipe')
    expect(rows[2]).toHaveTextContent('Outros')
  })

  it('a verified staff conversation keeps its own "Interna" badge', () => {
    renderPanel([conv('staff', { conversation_kind: 'internal', contact_kind: '' })])
    expect(screen.getByRole('listitem')).toHaveTextContent('Interna')
  })
})
