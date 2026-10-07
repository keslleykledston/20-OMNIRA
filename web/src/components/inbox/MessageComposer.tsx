import React, { useRef, useEffect, useState } from 'react';
import clsx from 'clsx';
import { Icon } from '../primitives';
import { ATTACHMENT_ACCEPT, UploadedAttachment, formatBytes } from '../../lib/attachments';

// Drafts live in memory per conversation: switching to another attendance must never carry the text over (it could go to the
// wrong person) nor lose what was typed for the previous one.
const drafts = new Map<string, string>();
// The file chosen for a conversation survives switching away and back, like the text (it is already uploaded and valid for 24 h).
const attachmentDrafts = new Map<string, UploadedAttachment>();

interface MessageComposerProps {
  /** Identifies whose draft this is (tenant + conversation). */
  draftKey?: string;
  // Returns whether the send actually succeeded — the composer only clears the
  // draft on a confirmed success, never optimistically (a 409 "must be
  // assigned first" must not silently drop what the operator typed).
  onSend: (text: string, attachmentId?: string) => Promise<boolean> | boolean;
  /** Present only when the server and the line can send files (ADR-0024): shows the paperclip. Rejects with the server's error. */
  onAttach?: (file: File) => Promise<UploadedAttachment>;
  /** Called when the operator takes an uploaded file out before sending. */
  onRemoveAttachment?: (attachmentId: string) => void;
  /** Turns an upload / send error into a sentence. */
  describeError?: (err: unknown) => string;
  disabled?: boolean;
  placeholder?: string;
  /** Small line above the box, e.g. "Responder ao contato · WhatsApp oficial". */
  label?: string;
  labelRight?: string;
}

export default function MessageComposer({
  draftKey = '',
  onSend,
  onAttach,
  onRemoveAttachment,
  describeError = () => 'Não foi possível anexar o arquivo.',
  disabled = false,
  placeholder = 'Escreva uma mensagem...',
  label,
  labelRight,
}: MessageComposerProps) {
  const textareaRef = useRef<HTMLTextAreaElement>(null);
  const [text, setTextState] = useState(() => drafts.get(draftKey) ?? '');
  const [attachment, setAttachmentState] = useState<UploadedAttachment | null>(() => attachmentDrafts.get(draftKey) ?? null);
  const [uploading, setUploading] = useState<string | null>(null); // file name while uploading
  const [attachError, setAttachError] = useState<string | null>(null);
  const fileInputRef = useRef<HTMLInputElement>(null);
  const keyRef = useRef(draftKey);
  const setAttachment = (value: UploadedAttachment | null, key = keyRef.current) => {
    setAttachmentState(value);
    if (value) attachmentDrafts.set(key, value);
    else attachmentDrafts.delete(key);
  };
  const setText = (value: string, key = keyRef.current) => {
    setTextState(value);
    if (value === '') drafts.delete(key);
    else drafts.set(key, value);
  };
  // another conversation: show ITS draft (the previous one stays saved under its own key)
  useEffect(() => {
    keyRef.current = draftKey;
    setTextState(drafts.get(draftKey) ?? '');
    setAttachmentState(attachmentDrafts.get(draftKey) ?? null);
    setAttachError(null);
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
    if ((text.trim() || attachment) && !disabled && !uploading) {
      const sentFrom = keyRef.current;
      const sent = await onSend(text, attachment?.id);
      // clear the draft of the conversation it was sent from, even if the person already switched away
      if (sent) {
        drafts.delete(sentFrom);
        attachmentDrafts.delete(sentFrom);
        if (keyRef.current === sentFrom) {
          setTextState('');
          setAttachmentState(null);
        }
      }
    }
  };

  const handleFile = async (file: File | undefined) => {
    if (!file || !onAttach) return;
    const from = keyRef.current;
    setAttachError(null);
    setUploading(file.name);
    try {
      const uploaded = await onAttach(file);
      // a file that finished uploading after the operator switched conversations belongs to the one it was chosen in
      if (keyRef.current === from) setAttachment(uploaded);
      else attachmentDrafts.set(from, uploaded);
    } catch (err) {
      if (keyRef.current === from) setAttachError(describeError(err));
    } finally {
      setUploading(null);
      if (fileInputRef.current) fileInputRef.current.value = '';
    }
  };

  const removeAttachment = () => {
    if (attachment) onRemoveAttachment?.(attachment.id);
    setAttachment(null);
  };

  const canSend = (!!text.trim() || !!attachment) && !disabled && !uploading;
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
      {attachError && (
        <div role="alert" className="rounded-control bg-status-danger-soft px-2 py-1.5 text-xs text-status-danger">
          {attachError}
        </div>
      )}
      {(uploading || attachment) && (
        <div className="flex items-center gap-2 rounded-control border border-border-subtle bg-surface-muted px-2 py-1.5 text-xs text-text-secondary" data-testid="attachment-chip">
          <Icon name="paperclip" size={14} />
          {uploading ? (
            <span role="status" className="truncate">Enviando e verificando {uploading}…</span>
          ) : (
            <>
              <span className="truncate font-medium text-text-primary">{attachment!.file_name}</span>
              <span className="flex-shrink-0 text-text-tertiary">{formatBytes(attachment!.size_bytes)}</span>
              <button type="button" onClick={removeAttachment} aria-label="Remover anexo" title="Remover anexo" className="ml-auto rounded p-0.5 text-text-tertiary hover:text-text-primary">
                <Icon name="close" size={14} />
              </button>
            </>
          )}
        </div>
      )}
      <div className="flex items-end gap-2 rounded-lg border border-border-subtle bg-surface p-2 focus-within:ring-2 focus-within:ring-accent-primary">
        {onAttach && (
          <>
            <input
              ref={fileInputRef}
              type="file"
              accept={ATTACHMENT_ACCEPT}
              className="hidden"
              data-testid="attachment-input"
              onChange={(e) => void handleFile(e.target.files?.[0])}
            />
            <button
              type="button"
              onClick={() => fileInputRef.current?.click()}
              disabled={disabled || !!uploading || !!attachment}
              aria-label="Anexar arquivo"
              title={attachment ? 'Um arquivo por mensagem' : 'Anexar arquivo'}
              className={clsx(
                'flex h-9 w-9 flex-shrink-0 items-center justify-center rounded-control text-text-tertiary transition-colors',
                disabled || uploading || attachment ? 'cursor-not-allowed opacity-50' : 'hover:bg-surface-muted hover:text-text-primary'
              )}
            >
              <Icon name="paperclip" className="w-4 h-4" />
            </button>
          </>
        )}
        <textarea
          ref={textareaRef}
          value={text}
          onChange={(e) => setText(e.target.value)}
          onKeyDown={handleKeyDown}
          disabled={disabled}
          placeholder={attachment ? 'Legenda (opcional)...' : placeholder}
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
