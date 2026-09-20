import { beforeEach, describe, expect, it, vi } from 'vitest';
import { screen, waitFor } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import axios from 'axios';
import ContactsPage, { formatPhone, initials } from '../pages/ContactsPage';
import ContactDetailPage from '../pages/ContactDetailPage';
import { renderAt, setSession, TENANT } from './testUtils';

vi.mock('axios');

const CONTACTS = `/api/v1/tenants/${TENANT}/contacts`;

const contact = (over: object = {}) => ({
  id: 'c1111111-1111-1111-1111-111111111111',
  display_name: 'Ana Souza',
  phone_e164: '+5511998887766',
  email: 'ana@example.com',
  status: 'active',
  created_at: '2026-01-10T12:00:00Z',
  updated_at: '2026-02-20T15:30:00Z',
  ...over,
});

const page = (items: object[], over: object = {}) => ({
  items,
  has_more: false,
  count: items.length,
  limit: 20,
  ...over,
});

describe('formatPhone', () => {
  it('formats a Brazilian mobile', () => {
    expect(formatPhone('+5511998887766')).toBe('+55 11 99888-7766');
  });

  // The API stores E.164 from any country; anything we cannot parse is shown raw
  // rather than mangled.
  it('leaves a non-Brazilian number untouched', () => {
    expect(formatPhone('+441632960961')).toBe('+441632960961');
  });
});

describe('initials', () => {
  it('uses first and last name', () => {
    expect(initials('Ana Souza')).toBe('AS');
  });
  it('falls back to two letters for a single name', () => {
    expect(initials('Ana')).toBe('AN');
  });
  it('survives an empty name', () => {
    expect(initials('   ')).toBe('?');
  });
});

describe('ContactsPage', () => {
  beforeEach(() => {
    vi.clearAllMocks();
    localStorage.clear();
    setSession();
  });

  // The table and the mobile cards are both in the DOM, hidden from each other
  // by CSS that jsdom does not apply — hence findAllByText throughout.
  it('lists the tenant contacts', async () => {
    vi.mocked(axios.get).mockResolvedValue({ data: page([contact()]) });
    renderAt(<ContactsPage />, '/contacts', '/contacts');

    expect((await screen.findAllByText('Ana Souza')).length).toBeGreaterThan(0);
    expect(screen.getAllByText('+55 11 99888-7766').length).toBeGreaterThan(0);
  });

  it('shows an empty state when the tenant has no contacts', async () => {
    vi.mocked(axios.get).mockResolvedValue({ data: page([]) });
    renderAt(<ContactsPage />, '/contacts', '/contacts');

    expect(await screen.findByText('Nenhum contato ainda')).toBeInTheDocument();
  });

  it('surfaces a failure with a retry', async () => {
    vi.mocked(axios.get).mockRejectedValue({ response: { status: 500 } });
    renderAt(<ContactsPage />, '/contacts', '/contacts');

    expect(await screen.findByText('Não foi possível carregar os contatos')).toBeInTheDocument();
    expect(screen.getByRole('button', { name: 'Tentar novamente' })).toBeInTheDocument();
  });

  it('walks pages with the cursor and can come back', async () => {
    const first = page([contact({ id: 'c1', display_name: 'Ana Souza' })], {
      has_more: true,
      next_cursor: 'CURSOR_2',
    });
    const second = page([contact({ id: 'c2', display_name: 'Bruno Lima' })]);

    vi.mocked(axios.get).mockImplementation(async (_url: string, config?: any) => ({
      data: config?.params?.cursor === 'CURSOR_2' ? second : first,
    }));

    renderAt(<ContactsPage />, '/contacts', '/contacts');
    await screen.findAllByText('Ana Souza');

    const next = screen.getByRole('button', { name: 'Próxima' });
    expect(next).toBeEnabled();
    await userEvent.click(next);

    expect((await screen.findAllByText('Bruno Lima')).length).toBeGreaterThan(0);
    // Last page: there is nowhere further to go.
    expect(screen.getByRole('button', { name: 'Próxima' })).toBeDisabled();

    await userEvent.click(screen.getByRole('button', { name: 'Anterior' }));
    expect((await screen.findAllByText('Ana Souza')).length).toBeGreaterThan(0);
  });

  it('disables Anterior on the first page', async () => {
    vi.mocked(axios.get).mockResolvedValue({ data: page([contact()]) });
    renderAt(<ContactsPage />, '/contacts', '/contacts');

    await screen.findAllByText('Ana Souza');
    expect(screen.getByRole('button', { name: 'Anterior' })).toBeDisabled();
  });
});

describe('ContactDetailPage', () => {
  beforeEach(() => {
    vi.clearAllMocks();
    localStorage.clear();
    setSession();
  });

  const detail = (id = 'c1') =>
    renderAt(<ContactDetailPage />, `/contacts/${id}`, '/contacts/:contactId');

  it('shows the contact profile', async () => {
    vi.mocked(axios.get).mockResolvedValue({ data: contact() });
    detail();

    expect(await screen.findByRole('heading', { name: 'Ana Souza' })).toBeInTheDocument();
    expect(screen.getByText('ana@example.com')).toBeInTheDocument();
    expect(screen.getByText('Ativo')).toBeInTheDocument();
  });

  it('marks a missing email instead of inventing one', async () => {
    vi.mocked(axios.get).mockResolvedValue({ data: contact({ email: '' }) });
    detail();

    expect(await screen.findByText('Não informado')).toBeInTheDocument();
  });

  // A contact of another tenant answers 404 exactly like an unknown id, so the
  // UI must not imply the record exists elsewhere.
  it('treats 404 as simply not found', async () => {
    vi.mocked(axios.get).mockRejectedValue({ response: { status: 404 } });
    detail();

    expect(await screen.findByText('Contato não encontrado.')).toBeInTheDocument();
  });

  it('copies the raw E.164 phone, not the formatted one', async () => {
    const writeText = vi.fn().mockResolvedValue(undefined);
    Object.assign(navigator, { clipboard: { writeText } });
    vi.mocked(axios.get).mockResolvedValue({ data: contact() });
    detail();

    await screen.findByRole('heading', { name: 'Ana Souza' });
    await userEvent.click(screen.getByRole('button', { name: 'Copiar telefone' }));

    await waitFor(() => expect(writeText).toHaveBeenCalledWith('+5511998887766'));
  });
});
