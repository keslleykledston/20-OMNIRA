import React from 'react';
import clsx from 'clsx';

type BadgeVariant = 'default' | 'success' | 'warning' | 'danger' | 'info';
type BadgeSize = 'sm' | 'md';

interface BadgeProps extends React.HTMLAttributes<HTMLSpanElement> {
  children: React.ReactNode;
  variant?: BadgeVariant;
  size?: BadgeSize;
}

export function Badge({
  children,
  variant = 'default',
  size = 'md',
  className,
  ...props
}: BadgeProps) {
  const sizeStyles = {
    sm: 'px-2 py-1 text-xs',
    md: 'px-3 py-1.5 text-sm',
  }[size];

  const variantStyles = {
    default: clsx(
      'bg-surface-muted',
      'text-text-primary',
      'border',
      'border-border-subtle'
    ),
    success: clsx(
      'bg-status-success-soft',
      'text-status-success',
      'border',
      'border-status-success-border'
    ),
    warning: clsx(
      'bg-status-warning-soft',
      'text-status-warning-strong',
      'border',
      'border-status-warning-border'
    ),
    danger: clsx(
      'bg-status-danger-soft',
      'text-status-danger',
      'border',
      'border-status-danger-border'
    ),
    info: clsx(
      'bg-status-info-soft',
      'text-status-info',
      'border',
      'border-status-info-border'
    ),
  }[variant];

  return (
    <span
      {...props}
      className={clsx(
        'inline-flex',
        'items-center',
        'gap-1.5',
        'rounded-full',
        'font-medium',
        'whitespace-nowrap',
        sizeStyles,
        variantStyles,
        className
      )}
    >
      {children}
    </span>
  );
}

export function StatusBadge({
  children,
  status,
  ...props
}: Omit<BadgeProps, 'variant'> & {
  status: 'success' | 'warning' | 'danger' | 'info' | 'default';
}) {
  return (
    <Badge {...props} variant={status as BadgeVariant}>
      {children}
    </Badge>
  );
}
