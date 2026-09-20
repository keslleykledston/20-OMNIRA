import React from 'react';
import clsx from 'clsx';
import { Button } from './Button';

/* Skeleton — subtle loading placeholder */
export function Skeleton({
  width = 'w-full',
  height = 'h-4',
  className,
  count = 1,
}: {
  width?: string;
  height?: string;
  className?: string;
  count?: number;
}) {
  return (
    <div className="space-y-3">
      {Array.from({ length: count }).map((_, idx) => (
        <div
          key={idx}
          className={clsx(
            'bg-surface-muted',
            'rounded-control',
            'animate-pulse',
            width,
            height,
            className
          )}
        />
      ))}
    </div>
  );
}

/* EmptyState — no data available */
interface EmptyStateProps {
  icon?: React.ReactNode;
  title: string;
  description?: string;
  action?: {
    label: string;
    onClick: () => void;
  };
}

export function EmptyState({
  icon,
  title,
  description,
  action,
}: EmptyStateProps) {
  return (
    <div className="flex flex-col items-center justify-center py-12 px-4 text-center">
      {icon && (
        <div className="mb-4 text-4xl">
          {icon}
        </div>
      )}
      <h3 className="text-lg font-semibold text-text-primary mb-2">
        {title}
      </h3>
      {description && (
        <p className="text-text-secondary mb-6 max-w-sm">
          {description}
        </p>
      )}
      {action && (
        <Button onClick={action.onClick} variant="primary" size="md">
          {action.label}
        </Button>
      )}
    </div>
  );
}

/* ErrorState — operation failed */
interface ErrorStateProps {
  title?: string;
  message: string;
  action?: {
    label: string;
    onClick: () => void;
  };
  isDismissible?: boolean;
  onDismiss?: () => void;
}

export function ErrorState({
  title = 'Erro ao carregar',
  message,
  action,
  isDismissible = false,
  onDismiss,
}: ErrorStateProps) {
  const [dismissed, setDismissed] = React.useState(false);

  if (dismissed) return null;

  const handleDismiss = () => {
    setDismissed(true);
    onDismiss?.();
  };

  return (
    <div className="bg-status-danger-soft border border-status-danger-border rounded-card p-4 md:p-6">
      <div className="flex gap-4">
        <div className="flex-shrink-0">
          <span className="text-2xl">⚠️</span>
        </div>
        <div className="flex-1">
          <h3 className="font-semibold text-status-danger mb-1">
            {title}
          </h3>
          <p className="text-status-danger text-sm mb-4">
            {message}
          </p>
          <div className="flex gap-2">
            {action && (
              <Button
                variant="danger"
                size="sm"
                onClick={action.onClick}
              >
                {action.label}
              </Button>
            )}
            {isDismissible && (
              <Button
                variant="secondary"
                size="sm"
                onClick={handleDismiss}
              >
                Descartar
              </Button>
            )}
          </div>
        </div>
      </div>
    </div>
  );
}

/* LoadingState — spinner with message */
export function LoadingState({
  message = 'Carregando...',
}: {
  message?: string;
}) {
  return (
    <div className="flex flex-col items-center justify-center py-12">
      <div className="w-8 h-8 border-4 border-accent-primary-soft border-t-accent-primary rounded-full animate-spin mb-4" />
      <p className="text-text-secondary">{message}</p>
    </div>
  );
}

/* PermissionState — insufficient access */
export function PermissionState({
  message = 'Você não tem permissão para acessar este recurso.',
}: {
  message?: string;
}) {
  return (
    <div className="bg-status-warning-soft border border-status-warning-border rounded-card p-6 text-center">
      <p className="text-lg font-semibold text-text-primary mb-2">🔒 Acesso restrito</p>
      <p className="text-text-secondary">{message}</p>
    </div>
  );
}
