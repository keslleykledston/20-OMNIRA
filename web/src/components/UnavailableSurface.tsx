import { EmptyState, Icon } from './primitives'

// Vite's built-in DEV flag: true only under `npm run dev`, always false for
// any build (`vite build` / `vite preview`, including production and the
// canonical E2E stack). Same precedent already used by
// TechnicianSelectModal.tsx for its own dev-only mock fallback — reused here
// rather than introducing a feature-flag framework.
export function isDevSurface(): boolean {
  return import.meta.env.DEV
}

/**
 * Rendered instead of a mock-backed page's fabricated fixture data outside
 * development. No fake business data, no backend call, no route removed —
 * the surface stays reachable, it just never presents fixtures as if real.
 */
export function UnavailableSurface({ title }: { title: string }) {
  return (
    <div className="p-6">
      <EmptyState
        icon={<Icon name="info" />}
        title={title}
        description="Este recurso ainda não está disponível nesta implantação."
      />
    </div>
  )
}
