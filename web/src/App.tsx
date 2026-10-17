import { createBrowserRouter, RouterProvider, Routes, Route, Navigate } from 'react-router-dom'
import { QueryClientProvider } from '@tanstack/react-query'
import { queryClient } from './lib/queryClient'
import Login from './pages/Login'
import NoAccess from './pages/NoAccess'
import Dashboard from './pages/Dashboard'
import Accounts from './pages/Accounts'
import Tickets from './pages/Tickets'
import TicketReconciliationPage from './pages/TicketReconciliationPage'
import Reports from './pages/Reports'
import SupervisorDashboard from './pages/SupervisorDashboard'
import ToConsole from './components/ToConsole'
import ConversationsEntry from './pages/ConversationsEntry'
import ContactsPage from './pages/ContactsPage'
import ContactDetailPage from './pages/ContactDetailPage'
import PeoplePage from './pages/PeoplePage'
import SettingsPage from './pages/SettingsPage'
import ExternalAppsPage from './pages/ExternalAppsPage'
import GroupsPage from './pages/GroupsPage'
import AttendanceSectorsPage from './pages/AttendanceSectorsPage'
import AttendanceCalendarsPage from './pages/AttendanceCalendarsPage'
import AttendanceShiftsPage from './pages/AttendanceShiftsPage'
import AttendanceOverflowPage from './pages/AttendanceOverflowPage'
import FlowsPage from './pages/FlowsPage'
import FlowEditorPage from './pages/FlowEditorPage'
import { RolesPermissionsPage } from './pages/RolesPermissionsPage'
import AcceptInvitePage from './pages/AcceptInvitePage'
import ChannelsPage from './pages/ChannelsPage'
import WahaWizardPage from './pages/WahaWizardPage'
import PrivacyPolicyPage from './pages/PrivacyPolicyPage'
import PasswordChangePage from './pages/PasswordChangePage'
import Layout from './components/Layout'



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
        <Route path="/inbox" element={<ConversationsEntry />} />
        {/* ADR-0039 (revisão 2026-10-09): "Conversas" é a única caixa; o Hub deixou de ser um destino à parte. Os endereços antigos continuam valendo. */}
        <Route path="/hub" element={<Navigate to="/inbox" replace />} />
        {/* ADR-0041: administração (acessos, instâncias e seus canais) vive no console, /admin/, uma entrada própria do build. Os endereços antigos redirecionam. */}
        <Route path="/hub/empresas" element={<ToConsole to="/acessos?aba=instancias" />} />
        <Route path="/acessos" element={<ToConsole />} />
        <Route path="/instancias/*" element={<ToConsole />} />
        <Route path="/groups" element={<GroupsPage />} />
        <Route path="/flows" element={<FlowsPage />} />
        <Route path="/flows/:flowId" element={<FlowEditorPage />} />
        <Route path="/contacts" element={<ContactsPage />} />
        <Route path="/contacts/:contactId" element={<ContactDetailPage />} />
        <Route path="/settings/password" element={<PasswordChangePage />} />
        <Route path="/settings/people" element={<PeoplePage />} />
        {/* Older addresses of the people screens (Equipe e acesso, Agentes) land on the one Pessoas page. */}
        <Route path="/settings/people/*" element={<Navigate to="/settings/people" replace />} />
        <Route path="/settings/team" element={<Navigate to="/settings/people" replace />} />
        <Route path="/settings/agents" element={<Navigate to="/settings/people" replace />} />
        <Route path="/settings/attendance" element={<Navigate to="/settings/attendance/sectors" replace />} />
        <Route path="/settings/attendance/sectors" element={<AttendanceSectorsPage />} />
        <Route path="/settings/attendance/calendars" element={<AttendanceCalendarsPage />} />
        <Route path="/settings/attendance/shifts" element={<AttendanceShiftsPage />} />
        <Route path="/settings/attendance/overflow" element={<AttendanceOverflowPage />} />
        <Route path="/settings/roles" element={<RolesPermissionsPage />} />
        <Route path="/settings/integrations" element={<Navigate to="/settings/integrations/apps" replace />} />
        <Route path="/settings/integrations/apps" element={<ExternalAppsPage />} />
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
