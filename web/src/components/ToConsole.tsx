import { useEffect } from 'react'
import { useLocation } from 'react-router-dom'

// Endereços antigos de administração (`/acessos`, `/instancias/...`) agora vivem no console (ADR-0041, /admin/): navegação completa, porque o console é
// outra entrada do build. `to` substitui o caminho quando o endereço antigo não tem equivalente direto.
export default function ToConsole({ to }: { to?: string }) {
  const { pathname, search } = useLocation()
  useEffect(() => {
    window.location.replace('/admin' + (to ?? pathname + search))
  }, [to, pathname, search])
  return null
}
