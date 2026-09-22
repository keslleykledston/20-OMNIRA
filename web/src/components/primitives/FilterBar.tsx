import React from 'react';
import clsx from 'clsx';

interface FilterBarProps extends React.HTMLAttributes<HTMLDivElement> {
  children: React.ReactNode;
}

/** Responsive layout slot for a search field + filter controls (composed by the
 *  caller — no hardcoded filters). Collapses to a stacked column below sm. */
export function FilterBar({ children, className, ...props }: FilterBarProps) {
  return (
    <div
      {...props}
      className={clsx('flex', 'flex-col', 'gap-3', 'sm:flex-row', 'sm:items-center', className)}
    >
      {children}
    </div>
  );
}
