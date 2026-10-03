import { describe, it, expect, beforeEach, vi } from 'vitest';
import axios from 'axios';
import { contactsAPI } from '../lib/contacts';

vi.mock('axios');

const TENANT = '11111111-1111-1111-1111-111111111111';
const CONTACT = '44444444-4444-4444-4444-444444444444';

describe('contactsAPI (Contact 360 read model)', () => {
  beforeEach(() => {
    vi.resetAllMocks();
    localStorage.setItem('tenantId', TENANT);
    localStorage.setItem('jwtToken', 'tok');
  });

  it('reads a contact conversations page from the tenant-scoped route', async () => {
    const page = { items: [], has_more: false, count: 0, limit: 20 };
    vi.mocked(axios.get).mockResolvedValue({ status: 200, data: page });
    await expect(contactsAPI.conversations(CONTACT)).resolves.toEqual(page);
    const [url, cfg] = vi.mocked(axios.get).mock.calls[0];
    expect(String(url)).toMatch(new RegExp(`/tenants/${TENANT}/contacts/${CONTACT}/conversations$`));
    expect(cfg?.params).toEqual({});
  });

  it('forwards cursor and limit only when provided', async () => {
    vi.mocked(axios.get).mockResolvedValue({ status: 200, data: { items: [], has_more: false, count: 0, limit: 5 } });
    await contactsAPI.conversations(CONTACT, 'abc', 5);
    expect(vi.mocked(axios.get).mock.calls[0][1]?.params).toEqual({ cursor: 'abc', limit: 5 });
  });

  it('reads tickets from the tickets route and surfaces a 403 to the caller', async () => {
    vi.mocked(axios.get).mockRejectedValue({ response: { status: 403 } });
    await expect(contactsAPI.tickets(CONTACT)).rejects.toMatchObject({ response: { status: 403 } });
    expect(String(vi.mocked(axios.get).mock.calls[0][0])).toMatch(
      new RegExp(`/tenants/${TENANT}/contacts/${CONTACT}/tickets$`),
    );
  });
});
