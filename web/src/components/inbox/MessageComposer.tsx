import React, { useRef, useEffect, useState } from 'react';
import clsx from 'clsx';
import { Icon } from '../primitives';

interface MessageComposerProps {
  // Returns whether the send actually succeeded — the composer only clears the
  // draft on a confirmed success, never optimistically (a 409 "must be
  // assigned first" must not silently drop what the operator typed).
  onSend: (text: string) => Promise<boolean> | boolean;
  disabled?: boolean;
  placeholder?: string;
  /** Small line above the box, e.g. "Responder ao contato · WhatsApp oficial". */
  label?: string;
  labelRight?: string;
}

export default function MessageComposer({
  onSend,
  disabled = false,
  placeholder = 'Escreva uma mensagem...',
  label,
  labelRight,
}: MessageComposerProps) {
  const textareaRef = useRef<HTMLTextAreaElement>(null);
  const [text, setText] = useState('');

  // Auto-grow textarea
  useEffect(() => {
    const textarea = textareaRef.current;
    if (textarea) {
      textarea.style.height = '40px';
      textarea.style.height = Math.min(textarea.scrollHeight, 120) + 'px';
    }
  }, [text]);

  const handleKeyDown = (e: React.KeyboardEvent<HTMLTextAreaElement>) => {
    if (e.key === 'Enter' && !e.shiftKey && !disabled) {
      e.preventDefault();
      handleSend();
    }
  };

  const handleSend = async () => {
    if (text.trim() && !disabled) {
      const sent = await onSend(text);
      if (sent) setText('');
    }
  };

  const canSend = !!text.trim() && !disabled;
  return (
    <div className="space-y-1.5">
      {label && (
        <div className="flex flex-wrap items-center justify-between gap-1 text-[10px] text-text-tertiary">
          <span className="flex items-center gap-1.5">
            <Icon name="conversations" size={12} />
            {label}
          </span>
          {labelRight && <span className="truncate">{labelRight}</span>}
        </div>
      )}
      <div className="flex items-end gap-2 rounded-lg border border-border-subtle bg-surface p-2 focus-within:ring-2 focus-within:ring-accent-primary">
        <textarea
          ref={textareaRef}
          value={text}
          onChange={(e) => setText(e.target.value)}
          onKeyDown={handleKeyDown}
          disabled={disabled}
          placeholder={placeholder}
          className={clsx(
            'flex-1 resize-none bg-transparent px-1 py-1.5 text-sm text-text-primary placeholder:text-text-tertiary',
            'focus:outline-none min-h-10 max-h-30',
            disabled && 'opacity-50 cursor-not-allowed'
          )}
        />
        <button
          onClick={handleSend}
          disabled={!canSend}
          aria-label="Enviar mensagem"
          title="Enviar mensagem"
          className={clsx(
            'flex h-9 w-9 flex-shrink-0 items-center justify-center rounded-control transition-colors',
            canSend ? 'bg-accent-primary text-white hover:bg-accent-primary-hover' : 'cursor-not-allowed bg-surface-muted text-text-tertiary'
          )}
        >
          <Icon name="arrow-up" className="w-4 h-4" />
        </button>
      </div>
    </div>
  );
}
