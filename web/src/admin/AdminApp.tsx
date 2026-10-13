import { createBrowserRouter, Navigate, Route, RouterProvider, Routes } from 'react-router-dom'
import { QueryClientProvider } from '@tanstack/react-query'
import { queryClient } from '../lib/queryClient'
import HubAccessPage from '../pages/HubAccessPage'
import HubManagedPage, { HubManagedChannelsRoute } from '../pages/HubManagedPage'
import AdminShell from './AdminShell'
import { useMyHubs } from '../hooks/useMyHubs'

// The console's front door: Access for Hub administrators, the instances' channels for whoever only received their management.
function ConsoleHome() {
  const hubs = useMyHubs().data ?? []
  const onlyChannels = !hubs.some((h) => h.can_manage_access) && hubs.some((h) => h.can_manage_instances)
  return <Navigate to={onlyChannels ? '/instancias' : '/acessos'} replace />
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
