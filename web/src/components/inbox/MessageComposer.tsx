import React, { useRef, useEffect, useState } from 'react';
import clsx from 'clsx';
import { Icon } from '../primitives';

// Drafts live in memory per conversation: switching to another attendance must never carry the text over (it could go to the
// wrong person) nor lose what was typed for the previous one.
const drafts = new Map<string, string>();

interface MessageComposerProps {
  /** Identifies whose draft this is (tenant + conversation). */
  draftKey?: string;
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
  draftKey = '',
  onSend,
  disabled = false,
  placeholder = 'Escreva uma mensagem...',
  label,
  labelRight,
}: MessageComposerProps) {
  const textareaRef = useRef<HTMLTextAreaElement>(null);
  const [text, setTextState] = useState(() => drafts.get(draftKey) ?? '');
  const keyRef = useRef(draftKey);
  const setText = (value: string, key = keyRef.current) => {
    setTextState(value);
    if (value === '') drafts.delete(key);
    else drafts.set(key, value);
  };
  // another conversation: show ITS draft (the previous one stays saved under its own key)
  useEffect(() => {
    keyRef.current = draftKey;
    setTextState(drafts.get(draftKey) ?? '');
  }, [draftKey]);

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
      const sentFrom = keyRef.current;
      const sent = await onSend(text);
      // clear the draft of the conversation it was sent from, even if the person already switched away
      if (sent) {
        drafts.delete(sentFrom);
        if (keyRef.current === sentFrom) setTextState('');
      }
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
