import { describe, it, expect, vi } from 'vitest';
import { render, screen } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { Table, TableHead, TableBody, TableRow, TableHeaderCell, TableCell } from '../components/primitives';

function renderTable(rowProps: { interactive?: boolean; selected?: boolean; onClick?: () => void } = {}) {
  return render(
    <Table>
      <TableHead>
        <TableRow>
          <TableHeaderCell>Nome</TableHeaderCell>
          <TableHeaderCell>Status</TableHeaderCell>
        </TableRow>
      </TableHead>
      <TableBody>
        <TableRow {...rowProps}>
          <TableCell>Ana</TableCell>
          <TableCell>Ativo</TableCell>
        </TableRow>
      </TableBody>
    </Table>
  );
}

describe('Table', () => {
  it('renders semantic table markup with headers and cells', () => {
    renderTable();
    expect(screen.getByRole('table')).toBeInTheDocument();
    expect(screen.getByRole('columnheader', { name: 'Nome' })).toBeInTheDocument();
    expect(screen.getByRole('cell', { name: 'Ana' })).toBeInTheDocument();
  });

  it('marks an interactive row as clickable and fires onClick', async () => {
    const onClick = vi.fn();
    renderTable({ interactive: true, onClick });
    await userEvent.click(screen.getByRole('cell', { name: 'Ana' }));
    expect(onClick).toHaveBeenCalledTimes(1);
  });

  it('exposes aria-selected on a selected row', () => {
    renderTable({ selected: true });
    const row = screen.getByRole('cell', { name: 'Ana' }).closest('tr');
    expect(row).toHaveAttribute('aria-selected', 'true');
  });
});
