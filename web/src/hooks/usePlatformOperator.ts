import { useQuery } from '@tanstack/react-query'
import { platformOperatorAPI } from '../lib/platformOperator'

// Whether the signed-in person is an active platform operator (ADR-0038) — decided ENTIRELY by
// the server, the same way useMyHubs decides what Hub nav to offer: a real call, and the server's
// answer decides what to show. A 404 here means "not a platform operator" (the uniform 404 every
// platform-wide handler gives to anyone else: a tenant admin, a Hub delegate, the API Headless).
// This hook only decides what to SHOW; every request a page makes afterwards is checked again by
// the server, and a manipulated/forged request gets the same 404.
//
// Achado real em produção (2026-11-15): isto costumava consultar Update Manager
// (`platformInstallationAPI.get`, /platform/installation) como um substituto de "sou operador?" —
// mas aquele endpoint só existe quando Update Manager está ligado (OMNIRA_PLATFORM_RELEASE_API_ENABLED).
// Num host onde essa OUTRA feature nunca foi ligada, a rota nem existia (404 cru do Go, nunca a
// resposta do handler), e um operador de verdade, já concedido no banco, via "Acesso restrito" no
// console inteiro — a checagem de operador nunca pode depender do estado de uma feature diferente.
// Agora usa /platform/operator-status, que não depende de nenhuma outra feature.
//
// THE ONLY place that defines the `['platform-operator-status']` query: AdminShell's nav guard AND
// PlatformReleasesPage both call THIS hook, never their own separate useQuery for the same key.
// Achado real (navegador real, nunca reproduzido em jsdom/Vitest): dois useQuery com queryFn por
// VALOR diferente para a MESMA chave faziam os dois observadores divergirem entre si (um
// refazendo o fetch ao montar via refetchOnMount, o outro já resolvido), deixando o status
// oscilar indefinidamente entre pending/error — a tela nunca saía de "Carregando…".
export function usePlatformOperator() {
  const query = useQuery({
    queryKey: ['platform-operator-status'],
    queryFn: platformOperatorAPI.get,
    retry: false,
    staleTime: 60_000,
  })
  const isOperator = query.isSuccess
  const isLoading = query.isLoading
  return { isOperator, isLoading, query }
}
