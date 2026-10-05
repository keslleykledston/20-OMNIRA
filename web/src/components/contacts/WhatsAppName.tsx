// The name the person declared on WhatsApp, shown smaller and grey BELOW the principal name (the team's alias, or the same
// WhatsApp name when nobody gave an alias). Rendered only when it differs from the principal name, so a contact without
// an alias shows one name, never the same name twice.
export function WhatsAppName({ principal, whatsapp, className = '' }: { principal?: string | null; whatsapp?: string | null; className?: string }) {
  const wa = (whatsapp ?? '').trim()
  if (!wa || wa === (principal ?? '').trim()) return null
  return (
    <span className={'block truncate text-[11px] font-normal text-text-tertiary ' + className} title={`Nome no WhatsApp: ${wa}`}>
      WhatsApp: {wa}
    </span>
  )
}
