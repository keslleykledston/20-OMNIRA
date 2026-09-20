import React from 'react';
import clsx from 'clsx';

interface PageHeaderProps {
  title: string;
  description?: string;
  breadcrumbs?: Array<{
    label: string;
    href?: string;
    current?: boolean;
  }>;
  actions?: React.ReactNode;
  className?: string;
}

export function PageHeader({
  title,
  description,
  breadcrumbs,
  actions,
  className,
}: PageHeaderProps) {
  return (
    <div className={clsx('bg-surface', 'border-b', 'border-border-subtle', 'px-6', 'py-6', className)}>
      {/* Breadcrumbs */}
      {breadcrumbs && breadcrumbs.length > 0 && (
        <nav className="mb-4 flex items-center gap-2 text-sm">
          {breadcrumbs.map((crumb, idx) => (
            <React.Fragment key={idx}>
              {idx > 0 && (
                <span className="text-text-tertiary">/</span>
              )}
              {crumb.href && !crumb.current ? (
                <a
                  href={crumb.href}
                  className="text-accent-primary hover:text-accent-primary-hover transition-colors"
                >
                  {crumb.label}
                </a>
              ) : (
                <span className={crumb.current ? 'text-text-primary font-medium' : 'text-text-secondary'}>
                  {crumb.label}
                </span>
              )}
            </React.Fragment>
          ))}
        </nav>
      )}

      {/* Title + Description + Actions */}
      <div className="flex items-start justify-between gap-4">
        <div className="flex-1">
          <h1 className="text-display-md font-bold text-text-primary mb-2">
            {title}
          </h1>
          {description && (
            <p className="text-text-secondary">
              {description}
            </p>
          )}
        </div>
        {actions && (
          <div className="flex gap-2 flex-shrink-0">
            {actions}
          </div>
        )}
      </div>
    </div>
  );
}
