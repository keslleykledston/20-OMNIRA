import { useContext, useEffect } from 'react'
import { UNSAFE_DataRouterContext, useBlocker } from 'react-router-dom'

export const UNSAVED_MESSAGE = 'Há alterações não salvas. Sair desta página descarta o rascunho local. Continuar?'

// Protege o rascunho local de uma saída acidental.
// - Fechar/recarregar a aba: beforeunload (sempre).
// - Com router de dados (createBrowserRouter, como o app): useBlocker intercepta TODA navegação que muda de página, inclusive o
//   botão "voltar/avançar" do navegador, links, a barra lateral e navigate().
// - Sem router de dados (ex.: MemoryRouter em testes), o useBlocker não existe: cai para a confirmação em cliques de link.
export default function UnsavedChangesGuard({ dirty }: { dirty: boolean }) {
  const inDataRouter = useContext(UNSAFE_DataRouterContext) != null
  useBeforeUnload(dirty)
  return inDataRouter ? <RouterBlocker dirty={dirty} /> : <ClickGuard dirty={dirty} />
}

function useBeforeUnload(dirty: boolean) {
  useEffect(() => {
    if (!dirty) return undefined
    const warn = (e: BeforeUnloadEvent) => { e.preventDefault(); e.returnValue = '' }
    window.addEventListener('beforeunload', warn)
    return () => window.removeEventListener('beforeunload', warn)
  }, [dirty])
}

function RouterBlocker({ dirty }: { dirty: boolean }) {
  const blocker = useBlocker(({ currentLocation, nextLocation }) => dirty && currentLocation.pathname !== nextLocation.pathname)
  useEffect(() => {
    if (blocker.state !== 'blocked') return
    if (window.confirm(UNSAVED_MESSAGE)) blocker.proceed()
    else blocker.reset()
  }, [blocker])
  return null
}

function ClickGuard({ dirty }: { dirty: boolean }) {
  useEffect(() => {
    if (!dirty) return undefined
    const onClick = (e: MouseEvent) => {
      if (e.defaultPrevented || e.button !== 0 || e.metaKey || e.ctrlKey || e.shiftKey || e.altKey) return
      const a = (e.target as Element | null)?.closest?.('a[href]') as HTMLAnchorElement | null
      if (!a || (a.target && a.target !== '_self') || a.hasAttribute('download')) return
      const url = new URL(a.href, window.location.href)
      if (url.origin !== window.location.origin) return // saída da aplicação: beforeunload cobre
      if (url.pathname === window.location.pathname && url.search === window.location.search) return
      if (!window.confirm(UNSAVED_MESSAGE)) {
        e.preventDefault()
        e.stopPropagation() // na captura, antes do onClick do <Link>: a navegação não acontece
      }
    }
    document.addEventListener('click', onClick, true)
    return () => document.removeEventListener('click', onClick, true)
  }, [dirty])
  return null
}
