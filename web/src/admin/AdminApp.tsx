import { createBrowserRouter, Navigate, Route, RouterProvider, Routes } from 'react-router-dom'
import { QueryClientProvider } from '@tanstack/react-query'
import { queryClient } from '../lib/queryClient'
import HubAccessPage from '../pages/HubAccessPage'
import HubManagedPage, { HubManagedChannelsRoute } from '../pages/HubManagedPage'
import PlatformReleasesPage from '../pages/PlatformReleasesPage'
import AdminShell from './AdminShell'
import { useMyHubs } from '../hooks/useMyHubs'
import { usePlatformOperator } from '../hooks/usePlatformOperator'

// The console's front door: Access for Hub administrators, the instances' channels for whoever only received their management, and
// Platform for whoever is only a platform operator (ADR-0038) with no Hub grant at all.
function ConsoleHome() {
  const hubs = useMyHubs().data ?? []
  const platformOperator = usePlatformOperator()
  const canAccess = hubs.some((h) => h.can_manage_access)
  const canManage = hubs.some((h) => h.can_manage_instances)
  if (!canAccess && !canManage && platformOperator.isOperator) return <Navigate to="/plataforma" replace />
  return <Navigate to={!canAccess && canManage ? '/instancias' : '/acessos'} replace />
}

// Os endereços são os mesmos de antes (`/acessos`, `/instancias/...`), agora sob o prefixo /admin do roteador (basename): os links internos das telas
// movidas continuam valendo sem edição.
export function AdminRoutes() {
  return (
    <Routes>
      <Route element={<AdminShell />}>
        <Route path="/" element={<ConsoleHome />} />
        <Route path="/acessos" element={<HubAccessPage />} />
        <Route path="/instancias" element={<HubManagedPage />} />
        <Route path="/instancias/:hubId/:tenantId/canais" element={<HubManagedChannelsRoute />} />
        <Route path="/instancias/:hubId/:tenantId/canais/whatsapp/new" element={<HubManagedChannelsRoute wizard />} />
        <Route path="/plataforma" element={<PlatformReleasesPage />} />
        <Route path="*" element={<Navigate to="/acessos" replace />} />
      </Route>
    </Routes>
  )
}

const router = createBrowserRouter([{ path: '*', element: <AdminRoutes /> }], { basename: '/admin' })

export default function AdminApp() {
  return (
    <QueryClientProvider client={queryClient}>
      <RouterProvider router={router} />
    </QueryClientProvider>
  )
}
