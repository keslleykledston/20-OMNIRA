import React from 'react';
import clsx from 'clsx';

interface CardProps extends React.HTMLAttributes<HTMLDivElement> {
  children: React.ReactNode;
  interactive?: boolean;
  padding?: 'compact' | 'normal' | 'large';
}

export function Card({
  children,
  interactive = false,
  padding = 'normal',
  className,
  ...props
}: CardProps) {
  const paddingStyles = {
    compact: 'p-4',
    normal: 'p-6',
    large: 'p-8',
  }[padding];

  return (
    <div
      {...props}
      className={clsx(
        'bg-surface',
        'border',
        'border-border-subtle',
        'rounded-card',
        'shadow-sm',
        paddingStyles,
        interactive && clsx(
          'cursor-pointer',
          'transition-colors',
          'hover:bg-surface-muted'
        ),
        className
      )}
    >
      {children}
    </div>
  );
}

interface CardHeaderProps extends React.HTMLAttributes<HTMLDivElement> {
  children: React.ReactNode;
}

export function CardHeader({ children, className, ...props }: CardHeaderProps) {
  return (
    <div
      {...props}
      className={clsx('pb-4', 'border-b', 'border-border-subtle', className)}
    >
      {children}
    </div>
  );
}

interface CardBodyProps extends React.HTMLAttributes<HTMLDivElement> {
  children: React.ReactNode;
}

export function CardBody({ children, className, ...props }: CardBodyProps) {
  return (
    <div {...props} className={clsx('py-4', className)}>
      {children}
    </div>
  );
}

interface CardFooterProps extends React.HTMLAttributes<HTMLDivElement> {
  children: React.ReactNode;
}

export function CardFooter({ children, className, ...props }: CardFooterProps) {
  return (
    <div
      {...props}
      className={clsx('pt-4', 'border-t', 'border-border-subtle', className)}
    >
      {children}
    </div>
  );
}
