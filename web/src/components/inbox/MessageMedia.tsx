import React, { useState } from 'react';
import clsx from 'clsx';
import { MessageItem } from '../../types/api';

interface MessageMediaProps {
  message: MessageItem;
  tenantId: string;
}

/**
 * MessageMedia renders media for a message safely:
 * - Raster images (jpeg, png, webp, gif) inline via authenticated OMNIRA endpoint
 * - Other media as downloadable attachments
 * - Fallback for missing or blocked media
 *
 * Security notes:
 * - Media is retrieved only through authenticated /api/v1/tenants/{id}/messages/{id}/media
 * - MediaRef (provider URL) is never exposed
 * - Server handles MIME sniffing and content safety validation
 */
export default function MessageMedia({ message, tenantId }: MessageMediaProps) {
  const [loading, setLoading] = useState(false);
  const [error, setError] = useState<string | null>(null);

  // Only render if message has media.
  if (!message.mime_type) {
    return null;
  }

  const mediaUrl = `/api/v1/tenants/${tenantId}/messages/${message.id}/media`;
  const isInlineImage = isImageType(message.mime_type);

  return (
    <div className="mt-2 max-w-xs">
      {error && (
        <div className="rounded bg-status-danger/10 p-2 text-xs text-status-danger">
          {error}
        </div>
      )}

      {isInlineImage ? (
        <MediaImage
          src={mediaUrl}
          alt={`Media from message ${message.id}`}
          onLoading={(isLoading) => setLoading(isLoading)}
          onError={(err) => setError(err)}
        />
      ) : (
        <MediaDownload
          href={mediaUrl}
          filename={sanitizeFilename(`media_${message.id}${getExtensionForMime(message.mime_type)}`)}
          mimeType={message.mime_type}
          sizeBytes={message.size_bytes}
        />
      )}
    </div>
  );
}

interface MediaImageProps {
  src: string;
  alt: string;
  onLoading?: (isLoading: boolean) => void;
  onError?: (error: string) => void;
}

function MediaImage({ src, alt, onLoading, onError }: MediaImageProps) {
  const [imageLoading, setImageLoading] = useState(true);

  return (
    <img
      src={src}
      alt={alt}
      className={clsx(
        'rounded max-w-full h-auto',
        imageLoading && 'opacity-50'
      )}
      onLoad={() => {
        setImageLoading(false);
        onLoading?.(false);
      }}
      onError={() => {
        setImageLoading(false);
        onError?.('Failed to load image');
      }}
    />
  );
}

interface MediaDownloadProps {
  href: string;
  filename: string;
  mimeType: string;
  sizeBytes?: number;
}

function MediaDownload({ href, filename, mimeType, sizeBytes }: MediaDownloadProps) {
  const filesize = sizeBytes ? formatFilesize(sizeBytes) : 'unknown size';
  const typeName = getTypeNameForMime(mimeType);

  return (
    <a
      href={href}
      download={filename}
      className={clsx(
        'block rounded border border-surface-muted bg-surface-subtle p-2 text-xs',
        'hover:bg-surface-hover no-underline'
      )}
    >
      <div className="flex items-center gap-2">
        <span className="text-lg">📎</span>
        <div className="flex-1 min-w-0">
          <div className="truncate font-medium text-text-primary">{filename}</div>
          <div className="text-text-secondary">{typeName} · {filesize}</div>
        </div>
        <span className="text-lg">↓</span>
      </div>
    </a>
  );
}

function isImageType(mimeType: string): boolean {
  const normalized = mimeType.split(';')[0].toLowerCase();
  return ['image/jpeg', 'image/png', 'image/webp', 'image/gif'].includes(normalized);
}

function getExtensionForMime(mimeType: string): string {
  const normalized = mimeType.split(';')[0].toLowerCase();
  const extensions: Record<string, string> = {
    'image/jpeg': '.jpg',
    'image/png': '.png',
    'image/webp': '.webp',
    'image/gif': '.gif',
    'application/pdf': '.pdf',
    'text/plain': '.txt',
    'application/json': '.json',
    'text/csv': '.csv',
  };
  return extensions[normalized] || '.bin';
}

function getTypeNameForMime(mimeType: string): string {
  const normalized = mimeType.split(';')[0].toLowerCase();
  const names: Record<string, string> = {
    'application/pdf': 'PDF',
    'text/plain': 'Text',
    'application/json': 'JSON',
    'text/csv': 'CSV',
    'image/jpeg': 'JPEG',
    'image/png': 'PNG',
    'image/webp': 'WebP',
    'image/gif': 'GIF',
  };
  return names[normalized] || 'File';
}

function formatFilesize(bytes: number): string {
  if (bytes === 0) return '0 B';
  const k = 1024;
  const sizes = ['B', 'KB', 'MB', 'GB'];
  const i = Math.floor(Math.log(bytes) / Math.log(k));
  return Math.round(bytes / Math.pow(k, i) * 10) / 10 + ' ' + sizes[i];
}

function sanitizeFilename(filename: string): string {
  // Remove path separators and control characters.
  return filename
    .replace(/[/\\]/g, '_')
    .replace(/\x00/g, '')
    .replace(/[\x00-\x1f\x7f]/g, '_');
}
