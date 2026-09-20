import React from 'react';
import clsx from 'clsx';

interface AvatarProps {
  src?: string;
  alt: string;
  initials?: string;
  size?: 'sm' | 'md' | 'lg';
  className?: string;
}

export function Avatar({
  src,
  alt,
  initials,
  size = 'md',
  className,
}: AvatarProps) {
  const sizeStyles = {
    sm: 'w-8 h-8 text-xs',
    md: 'w-10 h-10 text-sm',
    lg: 'w-12 h-12 text-base',
  }[size];

  return (
    <div
      className={clsx(
        'flex',
        'items-center',
        'justify-center',
        'rounded-full',
        'bg-accent-primary-soft',
        'text-accent-primary',
        'font-semibold',
        'overflow-hidden',
        'flex-shrink-0',
        sizeStyles,
        className
      )}
      title={alt}
    >
      {src ? (
        <img
          src={src}
          alt={alt}
          className="w-full h-full object-cover"
        />
      ) : (
        initials || alt.substring(0, 2).toUpperCase()
      )}
    </div>
  );
}

interface AvatarGroupProps {
  avatars: Array<{
    src?: string;
    alt: string;
    initials?: string;
  }>;
  max?: number;
  size?: 'sm' | 'md' | 'lg';
  className?: string;
}

export function AvatarGroup({
  avatars,
  max = 3,
  size = 'md',
  className,
}: AvatarGroupProps) {
  const visible = avatars.slice(0, max);
  const remaining = avatars.length - max;

  return (
    <div className={clsx('flex', 'items-center', 'gap-2', className)}>
      <div className="flex -space-x-2">
        {visible.map((avatar, idx) => (
          <div key={idx} className="ring-2 ring-surface">
            <Avatar
              {...avatar}
              size={size}
            />
          </div>
        ))}
      </div>
      {remaining > 0 && (
        <span className="text-sm text-text-secondary font-medium">
          +{remaining}
        </span>
      )}
    </div>
  );
}
