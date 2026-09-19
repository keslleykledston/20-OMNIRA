import { describe, it, expect, beforeEach, vi } from 'vitest';
import { render, screen, waitFor } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import axios from 'axios';
import { AssignmentButton } from '../components/AssignmentButton';

vi.mock('axios');

const TENANT = '11111111-1111-1111-1111-111111111111';
const CONV = '22222222-2222-2222-2222-222222222222';
const ME = '33333333-3333-3333-3333-333333333333';

describe('AssignmentButton', () => {
  beforeEach(() => {
    vi.resetAllMocks();
    localStorage.setItem('tenantId', TENANT);
    localStorage.setItem('jwtToken', 'tok');
  });

  it('claims via the inbox assign endpoint with an empty body', async () => {
    const onChange = vi.fn();
    vi.mocked(axios.post).mockResolvedValue({ status: 200, data: { assigned_to_user_id: ME, changed: true } });
    render(<AssignmentButton conversationId={CONV} onAssignmentChange={onChange} />);
    await userEvent.click(screen.getByText('Assign to Me'));
    await waitFor(() => expect(onChange).toHaveBeenCalledWith(ME));
    const [url, body, config] = vi.mocked(axios.post).mock.calls[0] as any[];
    expect(url).toBe(`/api/v1/tenants/${TENANT}/inbox/conversations/${CONV}/assign`);
    expect(body).toEqual({});
    expect(config.headers.Authorization).toBe('Bearer tok');
  });

  it('shows a clear message when another agent won the claim (409)', async () => {
    vi.mocked(axios.post).mockRejectedValue({ response: { status: 409, data: 'conversation already assigned to another agent' } });
    render(<AssignmentButton conversationId={CONV} />);
    await userEvent.click(screen.getByText('Assign to Me'));
    expect(await screen.findByText('Already assigned to another agent')).toBeInTheDocument();
  });

  it('releases via the inbox unassign endpoint', async () => {
    const onChange = vi.fn();
    vi.mocked(axios.post).mockResolvedValue({ status: 200, data: { assigned_to_user_id: null, changed: true } });
    render(<AssignmentButton conversationId={CONV} assignedToUserId={ME} onAssignmentChange={onChange} />);
    await userEvent.click(screen.getByText('Release'));
    await waitFor(() => expect(onChange).toHaveBeenCalledWith(''));
    expect(vi.mocked(axios.post).mock.calls[0][0]).toBe(`/api/v1/tenants/${TENANT}/inbox/conversations/${CONV}/unassign`);
  });

  it('shows a permission message on 403 when releasing', async () => {
    vi.mocked(axios.post).mockRejectedValue({ response: { status: 403, data: 'forbidden' } });
    render(<AssignmentButton conversationId={CONV} assignedToUserId={ME} />);
    await userEvent.click(screen.getByText('Release'));
    expect(await screen.findByText('You do not have permission to do this')).toBeInTheDocument();
  });
});
