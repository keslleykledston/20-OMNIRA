import React from 'react';
import clsx from 'clsx';

interface TableProps extends React.HTMLAttributes<HTMLDivElement> {
  children: React.ReactNode;
}

/** Thin wrapper over a native <table>: container + horizontal scroll containment.
 *  No sorting/filtering engine, no column config — compose <thead>/<tbody> as children. */
export function Table({ children, className, ...props }: TableProps) {
  return (
    <div
      {...props}
      className={clsx(
        'overflow-x-auto',
        'rounded-card',
        'border',
        'border-border-subtle',
        'bg-surface',
        className
      )}
    >
      <table className="w-full text-left">{children}</table>
    </div>
  );
}

interface TableHeadProps extends React.HTMLAttributes<HTMLTableSectionElement> {
  children: React.ReactNode;
}

export function TableHead({ children, className, ...props }: TableHeadProps) {
  return (
    <thead
      {...props}
      className={clsx('border-b', 'border-border-subtle', 'bg-surface-muted', className)}
    >
      {children}
    </thead>
  );
}

interface TableBodyProps extends React.HTMLAttributes<HTMLTableSectionElement> {
  children: React.ReactNode;
}

export function TableBody({ children, className, ...props }: TableBodyProps) {
  return (
    <tbody {...props} className={className}>
      {children}
    </tbody>
  );
}

interface TableRowProps extends React.HTMLAttributes<HTMLTableRowElement> {
  children: React.ReactNode;
  /** Row opens something on click — adds pointer/hover affordance. */
  interactive?: boolean;
  selected?: boolean;
}

export function TableRow({ children, interactive = false, selected = false, className, ...props }: TableRowProps) {
  return (
    <tr
      {...props}
      aria-selected={selected || undefined}
      className={clsx(
        'border-b',
        'border-border-subtle',
        'last:border-0',
        interactive && clsx('cursor-pointer', 'hover:bg-surface-hover', 'transition-colors'),
        selected && 'bg-accent-primary-soft',
        className
      )}
    >
      {children}
    </tr>
  );
}

interface TableHeaderCellProps extends React.ThHTMLAttributes<HTMLTableCellElement> {
  children?: React.ReactNode;
  /** Makes the column sortable: the direction it is ordered in now ('none' = not this column). Pair with `onSort`. */
  sortDirection?: 'asc' | 'desc' | 'none';
  /** Called when the person asks to change this column's order; the page decides the next state. */
  onSort?: () => void;
}

export function TableHeaderCell({ children, className, sortDirection, onSort, ...props }: TableHeaderCellProps) {
  const sortable = !!onSort;
  return (
    <th
      scope="col"
      {...props}
      aria-sort={sortable ? (sortDirection === 'asc' ? 'ascending' : sortDirection === 'desc' ? 'descending' : 'none') : undefined}
      className={clsx('px-6', 'py-3', 'text-sm', 'font-medium', 'text-text-secondary', className)}
    >
      {sortable ? (
        <button
          type="button"
          onClick={onSort}
          className={clsx(
            '-mx-2 inline-flex items-center gap-1 rounded-control px-2 py-1 font-medium transition-colors',
            'hover:bg-surface-hover hover:text-text-primary focus-visible:ring-2 focus-visible:ring-accent-primary',
            sortDirection && sortDirection !== 'none' && 'text-text-primary',
          )}
        >
          {children}
          <span aria-hidden="true" className={clsx('text-xs', sortDirection && sortDirection !== 'none' ? 'opacity-100' : 'opacity-30')}>
            {sortDirection === 'asc' ? '▲' : sortDirection === 'desc' ? '▼' : '↕'}
          </span>
        </button>
      ) : (
        children
      )}
    </th>
  );
}

interface TableCellProps extends React.TdHTMLAttributes<HTMLTableCellElement> {
  children?: React.ReactNode;
}

export function TableCell({ children, className, ...props }: TableCellProps) {
  return (
    <td {...props} className={clsx('px-6', 'py-4', className)}>
      {children}
    </td>
  );
}
