import React, { useRef, useEffect, useState } from 'react';
import clsx from 'clsx';
import { Icon } from '../primitives';

interface MessageComposerProps {
  onSend: (text: string) => void;
  disabled?: boolean;
  placeholder?: string;
}

export default function MessageComposer({
  onSend,
  disabled = false,
  placeholder = 'Escreva uma mensagem...'
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

  const handleSend = () => {
    if (text.trim() && !disabled) {
      onSend(text);
      setText('');
    }
  };

  return (
    <div className="flex gap-2 items-end">
      {/* Textarea */}
      <textarea
        ref={textareaRef}
        value={text}
        onChange={(e) => setText(e.target.value)}
        onKeyDown={handleKeyDown}
        disabled={disabled}
        placeholder={placeholder}
        className={clsx(
          'flex-1 px-3 py-2 text-sm rounded-control resize-none',
          'bg-surface-muted text-text-primary placeholder:text-text-tertiary',
          'focus:outline-none focus:ring-2 focus:ring-accent-primary',
          'border border-transparent',
          'min-h-10 max-h-30',
          disabled && 'opacity-50 cursor-not-allowed'
        )}
      />

      {/* Send Button */}
      <button
        onClick={handleSend}
        disabled={!text.trim() || disabled}
        className={clsx(
          'px-3 py-2 rounded-control font-medium transition-colors',
          'flex items-center gap-2 text-sm',
          !text.trim() || disabled
            ? 'bg-surface-muted text-text-tertiary cursor-not-allowed'
            : 'bg-accent-primary text-white hover:bg-accent-primary-hover'
        )}
      >
        <Icon name="arrow-up" className="w-4 h-4" />
        <span className="hidden sm:inline">Enviar</span>
      </button>
    </div>
  );
}
