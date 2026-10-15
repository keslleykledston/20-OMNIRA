function parse(iso?: string): Date | null {
  if (!iso) return null
  const d = new Date(iso)
  return Number.isNaN(d.getTime()) ? null : d
}

export function formatDateTime(iso?: string): string {
  const d = parse(iso)
  return d ? d.toLocaleString('pt-BR', { dateStyle: 'short', timeStyle: 'short' }) : ''
}

export function formatLastLogin(iso?: string): string {
  const d = parse(iso)
  if (!d) return 'nunca entrou'
  const time = d.toLocaleTimeString('pt-BR', { hour: '2-digit', minute: '2-digit' })
  if (d.toDateString() === new Date().toDateString()) return `hoje, ${time}`
  return `${d.toLocaleDateString('pt-BR', { day: '2-digit', month: 'short', year: 'numeric' })}, ${time}`
}

export function formatExpiresIn(iso: string): string {
  const d = parse(iso)
  if (!d) return '—'
  const diffMs = d.getTime() - Date.now()
  if (diffMs <= 0) return 'vencido'
  const hours = Math.round(diffMs / (1000 * 60 * 60))
  if (hours < 24) return `em ${hours} h`
  const days = Math.round(hours / 24)
  return `em ${days} ${days === 1 ? 'dia' : 'dias'}`
}

export function firstName(name: string): string {
  const base = name.includes('@') ? name.split('@')[0] : name
  return base.trim().split(/\s+/)[0] || name
}
