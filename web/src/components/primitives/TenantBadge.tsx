import { tenantCode } from '../../lib/tenants';

// Identity of the company being served: a short code plus the FULL name, so it never depends on colour alone.
// Colours come from the tenant tokens (--tenant-accent / --tenant-accent-muted), which sit on top of the OMNIRA palette.
export function TenantBadge({ name, className = '' }: { name: string; className?: string }) {
  return (
    <span className={`inline-flex min-w-0 items-center gap-1.5 ${className}`} title={name}>
      <span
        aria-hidden="true"
        className="flex-shrink-0 rounded-control px-1.5 py-0.5 text-[10px] font-bold tracking-wide"
        style={{ backgroundColor: 'var(--tenant-accent)', color: 'var(--color-surface)' }}
      >
        {tenantCode(name)}
      </span>
      <span className="truncate font-medium text-text-primary">{name}</span>
    </span>
  );
}
