import React from 'react';
import clsx from 'clsx';

type ButtonVariant = 'primary' | 'secondary' | 'tertiary' | 'danger' | 'success';
type ButtonSize = 'sm' | 'md' | 'lg';

interface ButtonProps extends React.ButtonHTMLAttributes<HTMLButtonElement> {
  variant?: ButtonVariant;
  size?: ButtonSize;
  isLoading?: boolean;
  children: React.ReactNode;
}

export function Button({
  variant = 'primary',
  size = 'md',
  isLoading = false,
  disabled = false,
  className,
  children,
  ...props
}: ButtonProps) {
  const baseStyles = clsx(
    'font-medium',
    'rounded-control',
    'inline-flex',
    'items-center',
    'justify-center',
    'gap-2',
    'transition-colors',
    'focus:outline-none',
    'focus-visible:ring-2',
    'focus-visible:ring-offset-2',
    'focus-visible:ring-accent-primary',
    'disabled:opacity-50',
    'disabled:cursor-not-allowed'
  );

  const sizeStyles = {
    sm: 'px-3 py-2 text-sm',
    md: 'px-4 py-3 text-sm',
    lg: 'px-6 py-4 text-base',
  }[size];

  const variantStyles = {
    primary: clsx(
      'bg-accent-primary',
      'text-white',
      'hover:bg-accent-primary-hover',
      'disabled:bg-accent-primary'
    ),
    secondary: clsx(
      'bg-surface-muted',
      'text-text-primary',
      'border',
      'border-border-subtle',
      'hover:bg-surface-hover',
      'disabled:bg-surface-muted'
    ),
    tertiary: clsx(
      'text-accent-primary',
      'hover:bg-accent-primary-soft',
      'disabled:text-text-tertiary'
    ),
    danger: clsx(
      'bg-status-danger',
      'text-white',
      'hover:bg-status-danger-hover',
      'disabled:bg-status-danger'
    ),
    success: clsx(
      'bg-status-success',
      'text-white',
      'hover:bg-status-success-hover',
      'disabled:bg-status-success'
    ),
  }[variant];

  return (
    <button
      {...props}
      disabled={disabled || isLoading}
      className={clsx(baseStyles, sizeStyles, variantStyles, className)}
    >
      {isLoading ? (
        <>
          <span className="inline-block w-4 h-4 border-2 border-transparent border-t-current rounded-full animate-spin" />
          {typeof children === 'string' ? '' : children}
        </>
      ) : (
        children
      )}
    </button>
  );
}

export function IconButton({
  variant = 'secondary',
  size = 'md',
  isLoading = false,
  disabled = false,
  className,
  children,
  ...props
}: Omit<ButtonProps, 'children'> & { children: React.ReactNode }) {
  const sizeStyles = {
    sm: 'p-2',
    md: 'p-2.5',
    lg: 'p-3',
  }[size];

  return (
    <Button
      {...props}
      variant={variant}
      disabled={disabled || isLoading}
      className={clsx('rounded-control', sizeStyles, className)}
    >
      {children}
    </Button>
  );
}
