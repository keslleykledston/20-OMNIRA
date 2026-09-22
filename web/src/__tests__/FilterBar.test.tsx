import { describe, it, expect } from 'vitest';
import { render, screen } from '@testing-library/react';
import { FilterBar } from '../components/primitives';

describe('FilterBar', () => {
  it('renders arbitrary filter controls composed by the caller', () => {
    render(
      <FilterBar>
        <input aria-label="Buscar" />
        <select aria-label="Status">
          <option value="all">Todos</option>
        </select>
      </FilterBar>
    );
    expect(screen.getByLabelText('Buscar')).toBeInTheDocument();
    expect(screen.getByLabelText('Status')).toBeInTheDocument();
  });
});
