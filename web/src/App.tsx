import { createBrowserRouter, RouterProvider, Routes, Route, Navigate } from 'react-router-dom'
import { QueryClientProvider, QueryClient } from '@tanstack/react-query'
import Login from './pages/Login'
import NoAccess from './pages/NoAccess'
import Dashboard from './pages/Dashboard'
import Accounts from './pages/Accounts'
import Tickets from './pages/Tickets'
import TicketReconciliationPage from './pages/TicketReconciliationPage'
import Reports from './pages/Reports'
import SupervisorDashboard from './pages/SupervisorDashboard'
import InboxWorkspace from './pages/InboxWorkspace'
import ContactsPage from './pages/ContactsPage'
import ContactDetailPage from './pages/ContactDetailPage'
import TeamPage from './pages/TeamPage'
import AgentsPage from './pages/AgentsPage'
import PeopleAndGroupsPage from './pages/PeopleAndGroupsPage'
import SettingsPage from './pages/SettingsPage'
import GroupsPage from './pages/GroupsPage'
import FlowsPage from './pages/FlowsPage'
import FlowEditorPage from './pages/FlowEditorPage'
import { RolesPermissionsPage } from './pages/RolesPermissionsPage'
import AcceptInvitePage from './pages/AcceptInvitePage'
import ChannelsPage from './pages/ChannelsPage'
import WahaWizardPage from './pages/WahaWizardPage'
import PrivacyPolicyPage from './pages/PrivacyPolicyPage'
import PasswordChangePage from './pages/PasswordChangePage'
import Layout from './components/Layout'

const queryClient = new QueryClient()


// Data router (createBrowserRouter) para termos useBlocker, que intercepta também o botão "voltar" do navegador (o BrowserRouter
// não permite). As rotas são as mesmas de antes: o <Routes> abaixo é descendente de uma única rota curinga.
function AppRoutes() {
  return (
    <Routes>
      <Route path="/privacidade" element={<PrivacyPolicyPage />} />
      <Route path="/login" element={<Login />} />
      <Route path="/no-access" element={<NoAccess />} />
      <Route path="/invite/:token" element={<AcceptInvitePage />} />
      <Route element={<Layout />}>
        <Route path="/" element={<Dashboard />} />
        <Route path="/accounts" element={<Accounts />} />
        <Route path="/tickets" element={<Tickets />} />
        <Route path="/ticket-reconciliation" element={<TicketReconciliationPage />} />
        <Route path="/reports" element={<Reports />} />
        <Route path="/supervisor" element={<SupervisorDashboard />} />
        <Route path="/inbox" element={<InboxWorkspace />} />
        <Route path="/groups" element={<GroupsPage />} />
        <Route path="/flows" element={<FlowsPage />} />
        <Route path="/flows/:flowId" element={<FlowEditorPage />} />
        <Route path="/contacts" element={<ContactsPage />} />
        <Route path="/contacts/:contactId" element={<ContactDetailPage />} />
        <Route path="/settings/password" element={<PasswordChangePage />} />
        <Route path="/settings/people/team" element={<TeamPage />} />
        <Route path="/settings/people/agents" element={<AgentsPage />} />
        <Route path="/settings/people" element={<Navigate to="/settings/people/team" replace />} />
        <Route path="/settings/roles" element={<RolesPermissionsPage />} />
        <Route path="/settings/general" element={<SettingsPage />} />
        {/* Legacy duplicate retired (DESIGN.5-A) — /channels is the sole Channels UI. */}
        <Route path="/integrations" element={<Navigate to="/channels" replace />} />
        <Route path="/channels" element={<ChannelsPage />} />
        <Route path="/channels/whatsapp/new" element={<WahaWizardPage />} />
        <Route path="*" element={<Navigate to="/" />} />
      </Route>
    </Routes>
  )
}

const router = createBrowserRouter([{ path: '*', element: <AppRoutes /> }])

export default function App() {
  return (
    <QueryClientProvider client={queryClient}>
      <RouterProvider router={router} />
    </QueryClientProvider>
  )
}
