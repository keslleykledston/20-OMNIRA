import { useEffect } from 'react'

export const UNSAVED_MESSAGE = 'Há alterações não salvas. Sair desta página descarta o rascunho local. Continuar?'

// Protege o rascunho local de uma saída acidental. O app usa BrowserRouter (não é data router), então não existe useBlocker:
// fechar/recarregar a aba é coberto por beforeunload, e a navegação interna por clique (link do editor, barra lateral) é
// confirmada aqui. O botão "voltar" do navegador não pode ser interceptado com segurança neste router.
export function useUnsavedGuard(dirty: boolean) {
  useEffect(() => {
    if (!dirty) return undefined
    const beforeUnload = (e: BeforeUnloadEvent) => { e.preventDefault(); e.returnValue = '' }
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
    window.addEventListener('beforeunload', beforeUnload)
    document.addEventListener('click', onClick, true)
    return () => {
      window.removeEventListener('beforeunload', beforeUnload)
      document.removeEventListener('click', onClick, true)
    }
  }, [dirty])
}
