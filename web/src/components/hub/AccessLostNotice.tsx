// What a tab shows when the person's access to that instance ended while the screen was open (ADR-0040 section 7).
// The server already refuses every further request; this is the visible part. Nothing of the instance is rendered here:
// the blurred shape is a placeholder, so no conversation, contact or name stays in the page.
export default function AccessLostNotice() {
  return (
    <div className="relative h-full min-h-[18rem] overflow-hidden">
      <div aria-hidden="true" className="absolute inset-0 grid grid-cols-[300px_minmax(0,1fr)] gap-px blur-sm select-none">
        <div className="space-y-3 border-r border-border-subtle p-4">
          {[0, 1, 2, 3, 4].map((i) => (
            <div key={i} className="h-14 rounded-control bg-surface-muted" />
          ))}
        </div>
        <div className="space-y-3 p-4">
          <div className="h-8 w-1/3 rounded-control bg-surface-muted" />
          <div className="h-16 w-2/3 rounded-control bg-surface-muted" />
          <div className="ml-auto h-12 w-1/2 rounded-control bg-surface-muted" />
        </div>
      </div>
      <div role="alert" className="relative flex h-full items-center justify-center p-6">
        <div className="max-w-sm rounded-card border border-border-subtle bg-surface p-6 text-center shadow-md">
          <h2 className="text-base font-semibold text-text-primary">Você não tem mais acesso a esta instância</h2>
          <p className="mt-2 text-sm text-text-secondary">Fale com o administrador do Hub para pedir a liberação.</p>
        </div>
      </div>
    </div>
  );
}
