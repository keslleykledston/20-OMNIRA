import { useQuery } from '@tanstack/react-query'
import { platformInstallationAPI } from '../lib/platformRelease'

// Whether the signed-in person is an active platform operator (ADR-0038) — decided ENTIRELY by
// the server, the same way useMyHubs decides what Hub nav to offer: a real call, and the server's
// answer decides what to show. A 404 here means "not a platform operator" (updatemgr/http.go
// `scope` gives the uniform 404 — never 403 — to anyone else: a tenant admin, a Hub delegate, the
// API Headless). This hook only decides what to SHOW; every request the page makes afterwards is
// checked again by the server, and a manipulated/forged request gets the same 404.
//
// THE ONLY place that defines the `['platform-installation']` query: AdminShell's nav guard AND
// PlatformReleasesPage both call THIS hook, never their own separate useQuery for the same key.
// Achado real (navegador real, nunca reproduzido em jsdom/Vitest): dois useQuery com queryFn por
// VALOR diferente para a MESMA chave faziam os dois observadores divergirem entre si (um
// refazendo o fetch ao montar via refetchOnMount, o outro já resolvido), deixando o status
// oscilar indefinidamente entre pending/error — a tela nunca saía de "Carregando…".
export function usePlatformOperator() {
  const query = useQuery({
    queryKey: ['platform-installation'],
    queryFn: platformInstallationAPI.get,
    retry: false,
    staleTime: 60_000,
  })
  const isOperator = query.isSuccess
  const isLoading = query.isLoading
  return { isOperator, isLoading, installation: query.data, query }
}
