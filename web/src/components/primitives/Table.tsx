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
}

export function TableHeaderCell({ children, className, ...props }: TableHeaderCellProps) {
  return (
    <th
      scope="col"
      {...props}
      className={clsx('px-6', 'py-3', 'text-sm', 'font-medium', 'text-text-secondary', className)}
    >
      {children}
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
