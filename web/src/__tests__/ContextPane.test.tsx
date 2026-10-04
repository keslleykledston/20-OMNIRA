import { beforeEach, describe, expect, it, vi } from 'vitest';
import { screen, waitFor } from '@testing-library/react';
import axios from 'axios';
import userEvent from '@testing-library/user-event';
import ContextPane from '../components/inbox/ContextPane';
import { renderAt, setSession } from './testUtils';

// PRODUCT.7B1C: proves the two confirmed misleading Inbox context elements
// are gone — the dead "Ver perfil 360°" CRM link (crm_contact_id, a K3G
// identity, pointed at /contacts/:id, an OMNIRA-internal contact id) and
// the unreachable "Participantes" section (conversation.participants is
// never populated by the canonical Inbox read) — while everything real
// (contact info, ticket context, CRM activity containment) keeps working.
vi.mock('axios');

const CONV = 'c-1';

const BASE_CONVERSATION = {
  id: CONV,
  contact_name: 'Maria Silva',
  contact_phone: '+5511999998888',
  status: 'active' as const,
  message_count: 3,
  unread_count: 0,
  assigned_to_user_id: 'user-1',
};

function mockConversation(overrides: Record<string, unknown> = {}) {
  const conversation = { ...BASE_CONVERSATION, ...overrides };
  vi.mocked(axios.get).mockImplementation(async (url: string) => {
    if (url.endsWith(`/inbox/conversations/${CONV}`)) return { data: conversation };
    if (url.endsWith('/crm/companies')) return { data: { items: [] } };
    if (url.endsWith('/ticket')) return { data: { local_ticket_id: 'lt-1', linked: false } };
    return Promise.reject({ response: { status: 404 } });
  });
}

beforeEach(() => {
  vi.clearAllMocks();
  setSession();
});

describe('ContextPane — PRODUCT.7B1C removal of misleading CRM context UI', () => {
  // A/B. no "Ver perfil 360°" text, and no link anywhere derived from
  // crm_contact_id — even when a conversation has one.
  it('never renders "Ver perfil 360°" or a link derived from crm_contact_id', async () => {
    mockConversation({ crm_contact_id: 'k3g-contact-999' });
    renderAt(<ContextPane conversationId={CONV} />);
    await screen.findByText('Maria Silva');
    expect(screen.queryByText('Ver perfil 360°')).not.toBeInTheDocument();
    const links = screen.queryAllByRole('link');
    expect(links.every((l) => !(l.getAttribute('href') || '').includes('k3g-contact-999'))).toBe(true);
    expect(links.every((l) => !(l.getAttribute('href') || '').includes('/contacts/'))).toBe(true);
  });

  // C. no "Participantes" section, even when the conversation payload
  // includes a populated participants array (proves removal, not just an
  // empty-array coincidence).
  it('never renders a "Participantes" section', async () => {
    mockConversation({ participants: [{ id: 'p1', user_id: 'agent-1', role: 'CO_ATTENDEE' }] });
    renderAt(<ContextPane conversationId={CONV} />);
    await screen.findByText('Maria Silva');
    expect(screen.queryByText('Participantes')).not.toBeInTheDocument();
  });

  // D. real contact information is preserved.
  it('still renders real contact information', async () => {
    mockConversation();
    renderAt(<ContextPane conversationId={CONV} />);
    await screen.findByText('Maria Silva');
    expect(screen.getByText('+5511999998888')).toBeInTheDocument();
  });

  // E. ticket context (TicketPanel) still renders.
  it('still renders ticket context', async () => {
    mockConversation();
    renderAt(<ContextPane conversationId={CONV} />);
    await screen.findByLabelText('Empresa');
  });

  // F. the CRM activity containment message (PRODUCT.7B1B) still renders
  // when the conversation has a linked CRM contact.
  it('still renders the CRM activity containment message when a CRM contact is linked', async () => {
    mockConversation({ crm_contact_id: 'k3g-contact-999' });
    renderAt(<ContextPane conversationId={CONV} />);
    await screen.findByText(/indispon[íi]vel/i);
  });

  // G. a conversation without crm_contact_id renders safely — no crash, no
  // containment message (there is nothing CRM-related to say yet).
  it('renders safely when the conversation has no crm_contact_id', async () => {
    mockConversation();
    renderAt(<ContextPane conversationId={CONV} />);
    await screen.findByText('Maria Silva');
    expect(screen.queryByText(/indispon[íi]vel/i)).not.toBeInTheDocument();
  });

  // H. this cleanup introduces no new backend/provider request: only the
  // pre-existing conversation/company/ticket reads happen, never a POST.
  it('introduces no new backend/provider request', async () => {
    mockConversation({ crm_contact_id: 'k3g-contact-999' });
    renderAt(<ContextPane conversationId={CONV} />);
    await screen.findByText('Maria Silva');
    await waitFor(() => expect(axios.get).toHaveBeenCalled());
    const urls = vi.mocked(axios.get).mock.calls.map((c) => c[0] as string);
    for (const url of urls) {
      expect(
        url.endsWith(`/inbox/conversations/${CONV}`) || url.endsWith('/crm/companies') || url.endsWith('/ticket'),
      ).toBe(true);
    }
    expect(axios.post).not.toHaveBeenCalled();
  });
});

describe('ContextPane — honest counters and unassigned hint', () => {
  // The API never used to send message_count, so the pane showed a permanent false "0".
  it('shows the message count from the API and a dash (never a false 0) when it is absent', async () => {
    mockConversation({ message_count: 11 });
    const { unmount } = renderAt(<ContextPane conversationId={CONV} />);
    await screen.findByText('Maria Silva');
    expect(screen.getByText('11')).toBeInTheDocument();
    unmount();

    mockConversation({ message_count: undefined });
    renderAt(<ContextPane conversationId={CONV} />);
    await screen.findByText('Maria Silva');
    expect(screen.queryByText('0')).not.toBeInTheDocument();
    expect(screen.getByText('Mensagens').nextElementSibling).toHaveTextContent('—');
  });

  // The chamado is shown to whoever holds the conversation; "no permission" for a
  // conversation nobody holds yet sends people hunting for a permission they have.
  it('tells you to take an unassigned conversation instead of claiming a missing permission', async () => {
    vi.mocked(axios.get).mockImplementation(async (url: string) => {
      if (url.endsWith(`/inbox/conversations/${CONV}`)) return { data: { ...BASE_CONVERSATION, assigned_to_user_id: undefined } };
      if (url.endsWith('/crm/companies')) return { data: { items: [] } };
      if (url.endsWith('/ticket')) return Promise.reject({ response: { status: 403, data: 'forbidden' } });
      return Promise.reject({ response: { status: 404 } });
    });
    renderAt(<ContextPane conversationId={CONV} />);
    expect(await screen.findByText('Assuma esta conversa para ver e criar o chamado.')).toBeInTheDocument();
    expect(screen.queryByText('Sem permissão para ver o chamado desta conversa.')).not.toBeInTheDocument();
  });

  it('keeps the permission message when the conversation IS assigned and the ticket is still refused', async () => {
    vi.mocked(axios.get).mockImplementation(async (url: string) => {
      if (url.endsWith(`/inbox/conversations/${CONV}`)) return { data: BASE_CONVERSATION };
      if (url.endsWith('/crm/companies')) return { data: { items: [] } };
      if (url.endsWith('/ticket')) return Promise.reject({ response: { status: 403, data: 'forbidden' } });
      return Promise.reject({ response: { status: 404 } });
    });
    renderAt(<ContextPane conversationId={CONV} />);
    expect(await screen.findByText('Sem permissão para ver o chamado desta conversa.')).toBeInTheDocument();
  });
});

describe('ContextPane — contact classification (ADR-0014)', () => {
  it('shows the control for the conversation\'s contact and saves through the contacts API', async () => {
    mockConversation({ contact_id: 'contact-9', contact_kind: 'other' });
    vi.mocked(axios.patch).mockResolvedValue({ data: {} });
    renderAt(<ContextPane conversationId={CONV} />);
    await screen.findByText('Maria Silva');
    const user = userEvent.setup();
    await user.click(await screen.findByRole('button', { name: 'Cliente' }));
    await waitFor(() => expect(axios.patch).toHaveBeenCalled());
    expect(vi.mocked(axios.patch).mock.calls[0][0]).toMatch(/\/contacts\/contact-9$/);
    expect((vi.mocked(axios.patch).mock.calls[0][1] as { kind: string }).kind).toBe('customer');
  });

  it('shows the spam state for a spam contact', async () => {
    mockConversation({ contact_id: 'contact-9', contact_kind: 'spam' });
    renderAt(<ContextPane conversationId={CONV} />);
    expect(await screen.findByText('Marcado como spam')).toBeInTheDocument();
  });

  it('shows no control when the conversation carries no contact id (nothing to classify)', async () => {
    mockConversation({});
    renderAt(<ContextPane conversationId={CONV} />);
    await screen.findByText('Maria Silva');
    expect(screen.queryByText('Tipo de contato')).not.toBeInTheDocument();
  });
});

describe('ContextPane — spam contact has nothing to attend (ADR-0014)', () => {
  it('hides Assumir / Transferir for a spam contact, and shows them for a normal one', async () => {
    mockConversation({ contact_id: 'contact-9', contact_kind: 'spam', assigned_to_user_id: undefined });
    const { unmount } = renderAt(<ContextPane conversationId={CONV} />);
    await screen.findByText('Marcado como spam');
    expect(screen.queryByRole('button', { name: /Assumir/ })).not.toBeInTheDocument();
    expect(screen.queryByRole('button', { name: /Transferir/ })).not.toBeInTheDocument();
    unmount();

    mockConversation({ contact_id: 'contact-9', contact_kind: 'other', assigned_to_user_id: undefined });
    renderAt(<ContextPane conversationId={CONV} />);
    expect(await screen.findByRole('button', { name: /Assumir/ })).toBeInTheDocument();
    expect(screen.getByRole('button', { name: /Transferir/ })).toBeInTheDocument();
  });
});
