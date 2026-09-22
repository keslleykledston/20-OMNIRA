import React from 'react';
import clsx from 'clsx';

interface PaginationProps {
  onPrevious: () => void;
  onNext: () => void;
  hasPrevious: boolean;
  hasNext: boolean;
  /** e.g. "20 contatos nesta página" — no fake total-page count, the API is cursor-based. */
  label?: React.ReactNode;
  previousLabel?: string;
  nextLabel?: string;
  className?: string;
}

/** Cursor-style pagination: Previous/Next only, matching the backend's cursor
 *  pagination — never page numbers or a computed total page count. */
export function Pagination({
  onPrevious,
  onNext,
  hasPrevious,
  hasNext,
  label,
  previousLabel = 'Anterior',
  nextLabel = 'Próxima',
  className,
}: PaginationProps) {
  const buttonClass =
    'h-10 px-4 rounded-control border border-border-light text-sm font-medium text-text-primary hover:bg-surface-hover disabled:opacity-40 disabled:cursor-not-allowed transition-colors';

  return (
    <nav className={clsx('flex', 'items-center', 'justify-between', className)} aria-label="Paginação">
      {label ? <p className="text-sm text-text-secondary">{label}</p> : <span />}
      <div className="flex gap-2">
        <button type="button" onClick={onPrevious} disabled={!hasPrevious} className={buttonClass}>
          {previousLabel}
        </button>
        <button type="button" onClick={onNext} disabled={!hasNext} className={buttonClass}>
          {nextLabel}
        </button>
      </div>
    </nav>
  );
}
