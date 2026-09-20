import React from 'react';
import clsx from 'clsx';

interface InputProps extends React.InputHTMLAttributes<HTMLInputElement> {
  label?: string;
  error?: string;
  helperText?: string;
  isLoading?: boolean;
}

export function Input({
  label,
  error,
  helperText,
  isLoading,
  disabled,
  className,
  id,
  ...props
}: InputProps) {
  const inputId = id || `input-${Math.random().toString(36).substr(2, 9)}`;

  return (
    <div className="w-full">
      {label && (
        <label
          htmlFor={inputId}
          className={clsx(
            'block',
            'text-sm',
            'font-medium',
            'mb-2',
            error ? 'text-status-danger' : 'text-text-primary'
          )}
        >
          {label}
        </label>
      )}
      <input
        {...props}
        id={inputId}
        disabled={disabled || isLoading}
        className={clsx(
          'w-full',
          'px-4',
          'py-3',
          'text-text-primary',
          'bg-surface',
          'border',
          'rounded-control',
          'transition-colors',
          'focus:outline-none',
          'focus-visible:ring-2',
          'focus-visible:ring-accent-primary',
          error
            ? clsx(
              'border-status-danger',
              'focus-visible:border-status-danger',
              'focus-visible:ring-status-danger-border'
            )
            : clsx(
              'border-border-subtle',
              'focus-visible:border-accent-primary',
              'hover:border-border-light'
            ),
          'disabled:bg-surface-muted',
          'disabled:cursor-not-allowed',
          'disabled:text-text-tertiary',
          'placeholder:text-text-tertiary',
          className
        )}
      />
      {error && (
        <p className="mt-1 text-sm text-status-danger">{error}</p>
      )}
      {helperText && !error && (
        <p className="mt-1 text-sm text-text-tertiary">{helperText}</p>
      )}
    </div>
  );
}

interface SearchFieldProps extends Omit<InputProps, 'type' | 'label'> {
  placeholder?: string;
  onClear?: () => void;
}

export function SearchField({
  placeholder = 'Pesquisar...',
  onClear,
  value,
  ...props
}: SearchFieldProps) {
  return (
    <div className="relative w-full">
      <Input
        {...props}
        type="text"
        placeholder={placeholder}
        value={value}
        className="pl-10"
      />
      <span className="absolute left-3 top-1/2 transform -translate-y-1/2 text-text-tertiary">
        🔍
      </span>
      {value && onClear && (
        <button
          type="button"
          onClick={onClear}
          className="absolute right-3 top-1/2 transform -translate-y-1/2 text-text-tertiary hover:text-text-primary"
          aria-label="Limpar pesquisa"
        >
          ✕
        </button>
      )}
    </div>
  );
}

interface TextAreaProps extends React.TextareaHTMLAttributes<HTMLTextAreaElement> {
  label?: string;
  error?: string;
  helperText?: string;
}

export function TextArea({
  label,
  error,
  helperText,
  disabled,
  className,
  id,
  ...props
}: TextAreaProps) {
  const textareaId = id || `textarea-${Math.random().toString(36).substr(2, 9)}`;

  return (
    <div className="w-full">
      {label && (
        <label
          htmlFor={textareaId}
          className={clsx(
            'block',
            'text-sm',
            'font-medium',
            'mb-2',
            error ? 'text-status-danger' : 'text-text-primary'
          )}
        >
          {label}
        </label>
      )}
      <textarea
        {...props}
        id={textareaId}
        disabled={disabled}
        className={clsx(
          'w-full',
          'px-4',
          'py-3',
          'text-text-primary',
          'bg-surface',
          'border',
          'rounded-control',
          'transition-colors',
          'resize-none',
          'focus:outline-none',
          'focus-visible:ring-2',
          'focus-visible:ring-accent-primary',
          error
            ? clsx(
              'border-status-danger',
              'focus-visible:border-status-danger',
              'focus-visible:ring-status-danger-border'
            )
            : clsx(
              'border-border-subtle',
              'focus-visible:border-accent-primary',
              'hover:border-border-light'
            ),
          'disabled:bg-surface-muted',
          'disabled:cursor-not-allowed',
          'disabled:text-text-tertiary',
          'placeholder:text-text-tertiary',
          className
        )}
      />
      {error && (
        <p className="mt-1 text-sm text-status-danger">{error}</p>
      )}
      {helperText && !error && (
        <p className="mt-1 text-sm text-text-tertiary">{helperText}</p>
      )}
    </div>
  );
}
