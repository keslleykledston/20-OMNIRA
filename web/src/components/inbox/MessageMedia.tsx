import React, { useState } from 'react';
import clsx from 'clsx';
import { MessageItem } from '../../types/api';
import { Icon } from '../primitives';

interface MessageMediaProps {
  message: MessageItem;
  tenantId: string;
}

/**
 * Renders a message's attachment according to where it is in the security pipeline (ADR-0016):
 * - only a file the antivirus cleared is ever requested from the server;
 * - raster images show inline, audio and video get a native player, documents are downloads;
 * - anything else says plainly why there is nothing to open (checking, blocked, no longer available).
 *
 * The provider URL never reaches the browser; the file comes only through the authenticated endpoint.
 */
export default function MessageMedia({ message, tenantId }: MessageMediaProps) {
  const [error, setError] = useState(false);

  if (!message.mime_type) return null;

  const url = `/api/v1/tenants/${tenantId}/messages/${message.id}/media`;
  const status = message.media_status;

  // Older servers send no media_status: keep the previous behaviour of trying to load it.
  const gap = message.body ? 'mt-2' : '';
  if (status && status !== 'clean') return <MediaNotice status={status} gap={gap} />;
  if (error) return <MediaNotice status="source_gone" gap={gap} />;

  const kind = kindOf(message.mime_type);
  return (
    <div className={clsx('max-w-xs', gap)}>
      {kind === 'image' && (
        <img
          src={url}
          alt="Imagem recebida"
          className="h-auto max-w-full rounded"
          loading="lazy"
          onError={() => setError(true)}
        />
      )}
      {kind === 'audio' && (
        <audio controls preload="metadata" src={url} className="w-full min-w-[14rem]" onError={() => setError(true)}>
          Seu navegador não reproduz este áudio.
        </audio>
      )}
      {kind === 'video' && (
        <video controls preload="metadata" src={url} className="max-w-full rounded" onError={() => setError(true)} />
      )}
      {kind === 'file' && (
        <MediaDownload
          href={url}
          filename={safeName(`arquivo_${message.id.slice(0, 8)}${extensionFor(message.mime_type)}`)}
          mimeType={message.mime_type}
          sizeBytes={message.size_bytes}
        />
      )}
    </div>
  );
}

const NOTICE: Record<string, { text: string; danger: boolean }> = {
  pending: { text: 'Verificando o arquivo…', danger: false },
  quarantined: { text: 'Verificando o arquivo…', danger: false },
  infected: { text: 'Arquivo bloqueado: o antivírus detectou uma ameaça.', danger: true },
  rejected: { text: 'Arquivo bloqueado: tipo de arquivo não permitido.', danger: true },
  failed: { text: 'Não foi possível verificar este arquivo, por segurança ele não foi liberado.', danger: true },
  source_gone: { text: 'Arquivo indisponível: não foi guardado a tempo ou já foi removido.', danger: false },
};

function MediaNotice({ status, gap }: { status: string; gap: string }) {
  const notice = NOTICE[status] ?? NOTICE.source_gone;
  return (
    <div
      role={notice.danger ? 'alert' : 'status'}
      className={clsx(
        'flex max-w-xs items-start gap-1.5 rounded px-2 py-1.5 text-xs',
        gap,
        notice.danger ? 'bg-status-danger/10 text-status-danger' : 'bg-surface-subtle text-text-secondary',
      )}
    >
      <Icon name={status === 'pending' || status === 'quarantined' ? 'clock' : 'info'} size={14} className="mt-px" />
      <span>{notice.text}</span>
    </div>
  );
}

function MediaDownload({ href, filename, mimeType, sizeBytes }: { href: string; filename: string; mimeType: string; sizeBytes?: number }) {
  return (
    <a
      href={href}
      download={filename}
      className="block rounded border border-surface-muted bg-surface-subtle p-2 text-xs no-underline hover:bg-surface-hover"
    >
      <div className="flex items-center gap-2">
        <span className="text-lg" aria-hidden="true">📎</span>
        <div className="min-w-0 flex-1">
          <div className="truncate font-medium text-text-primary">{filename}</div>
          <div className="text-text-secondary">{typeName(mimeType)} · {sizeBytes ? formatSize(sizeBytes) : 'tamanho desconhecido'}</div>
        </div>
        <span className="text-lg" aria-hidden="true">↓</span>
      </div>
    </a>
  );
}

function base(mime: string): string {
  return mime.split(';')[0].trim().toLowerCase();
}

function kindOf(mime: string): 'image' | 'audio' | 'video' | 'file' {
  const m = base(mime);
  if (['image/jpeg', 'image/png', 'image/webp', 'image/gif'].includes(m)) return 'image';
  if (m.startsWith('audio/')) return 'audio';
  if (m.startsWith('video/')) return 'video';
  return 'file';
}

function extensionFor(mime: string): string {
  const map: Record<string, string> = {
    'application/pdf': '.pdf',
    'text/plain': '.txt',
    'text/csv': '.csv',
    'image/jpeg': '.jpg',
    'image/png': '.png',
  };
  return map[base(mime)] ?? '.bin';
}

function typeName(mime: string): string {
  const map: Record<string, string> = { 'application/pdf': 'PDF', 'text/plain': 'Texto', 'text/csv': 'CSV' };
  return map[base(mime)] ?? 'Arquivo';
}

function formatSize(bytes: number): string {
  if (bytes < 1024) return `${bytes} B`;
  const units = ['KB', 'MB', 'GB'];
  let value = bytes / 1024;
  let i = 0;
  while (value >= 1024 && i < units.length - 1) {
    value /= 1024;
    i++;
  }
  return `${Math.round(value * 10) / 10} ${units[i]}`;
}

function safeName(name: string): string {
  return name.replace(/[/\\]/g, '_').replace(/[\x00-\x1f\x7f]/g, '_');
}
