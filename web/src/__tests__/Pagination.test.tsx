import { describe, it, expect, vi } from 'vitest';
import { render, screen } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { Pagination } from '../components/primitives';

describe('Pagination', () => {
  it('disables Previous when there is no previous page', () => {
    render(<Pagination hasPrevious={false} hasNext={true} onPrevious={vi.fn()} onNext={vi.fn()} />);
    expect(screen.getByRole('button', { name: 'Anterior' })).toBeDisabled();
    expect(screen.getByRole('button', { name: 'Próxima' })).toBeEnabled();
  });

  it('disables Next when there is no next page', () => {
    render(<Pagination hasPrevious={true} hasNext={false} onPrevious={vi.fn()} onNext={vi.fn()} />);
    expect(screen.getByRole('button', { name: 'Próxima' })).toBeDisabled();
  });

  it('calls onNext/onPrevious on click', async () => {
    const onNext = vi.fn();
    const onPrevious = vi.fn();
    render(<Pagination hasPrevious={true} hasNext={true} onPrevious={onPrevious} onNext={onNext} />);
    await userEvent.click(screen.getByRole('button', { name: 'Próxima' }));
    await userEvent.click(screen.getByRole('button', { name: 'Anterior' }));
    expect(onNext).toHaveBeenCalledTimes(1);
    expect(onPrevious).toHaveBeenCalledTimes(1);
  });

  it('renders an optional label without computing a fake page total', () => {
    render(<Pagination hasPrevious={false} hasNext={false} onPrevious={vi.fn()} onNext={vi.fn()} label="20 contatos nesta página" />);
    expect(screen.getByText('20 contatos nesta página')).toBeInTheDocument();
  });
});
