import { useMyTenants } from '../../hooks/useMyTenants';
import { getTenantId } from '../../lib/session';
import { tenantDisplayName } from '../../lib/tenants';
import { TenantBadge } from '../primitives/TenantBadge';

// A thin strip that says WHICH company this conversation belongs to. It exists for people who serve more than one
// company (an operator of a BPO / service desk), where answering as the wrong company is the costly mistake.
// Someone who belongs to a single company sees nothing new. The company comes from the signed-in session and the
// server-provided list; it is display only and never decides access.
export default function TenantContextBar() {
  const tenants = useMyTenants();
  const list = tenants.data ?? [];
  const current = list.find((t) => t.id === getTenantId());
  if (list.length < 2 || !current) return null;
  return (
    <div
      data-testid="tenant-context-bar"
      aria-label={`Atendendo a empresa ${tenantDisplayName(current)}`}
      className="flex items-center gap-2 border-b border-border-subtle px-4 py-1 text-[11px]"
      style={{ backgroundColor: 'var(--tenant-accent-muted)' }}
    >
      <span className="text-text-secondary">Atendendo</span>
      <TenantBadge name={tenantDisplayName(current)} />
    </div>
  );
}
