import { BrowserRouter, Routes, Route, Navigate, useParams } from 'react-router-dom'
import { QueryClientProvider, QueryClient } from '@tanstack/react-query'
import Login from './pages/Login'
import NoAccess from './pages/NoAccess'
import Dashboard from './pages/Dashboard'
import Accounts from './pages/Accounts'
import Tickets from './pages/Tickets'
import Reports from './pages/Reports'
import SupervisorDashboard from './pages/SupervisorDashboard'
import { InboxPage } from './pages/InboxPage'
import { ConversationPage } from './pages/ConversationPage'
import IntegrationsPage from './pages/IntegrationsPage'
import ContactsPage from './pages/ContactsPage'
import ContactDetailPage from './pages/ContactDetailPage'
import TeamPage from './pages/TeamPage'
import ChannelsPage from './pages/ChannelsPage'
import WahaWizardPage from './pages/WahaWizardPage'
import Layout from './components/Layout'

const queryClient = new QueryClient()

function ConversationRoute() {
  const { conversationId = '' } = useParams()
  return <ConversationPage key={conversationId} conversationId={conversationId} />
}

export default function App() {
  return (
    <QueryClientProvider client={queryClient}>
      <BrowserRouter>
        <Routes>
          <Route path="/login" element={<Login />} />
          <Route path="/no-access" element={<NoAccess />} />
          <Route element={<Layout />}>
            <Route path="/" element={<Dashboard />} />
            <Route path="/accounts" element={<Accounts />} />
            <Route path="/tickets" element={<Tickets />} />
            <Route path="/reports" element={<Reports />} />
            <Route path="/supervisor" element={<SupervisorDashboard />} />
            <Route path="/inbox" element={<InboxPage />} />
            <Route path="/contacts" element={<ContactsPage />} />
            <Route path="/contacts/:contactId" element={<ContactDetailPage />} />
            <Route path="/settings/team" element={<TeamPage />} />
            <Route path="/integrations" element={<IntegrationsPage />} />
            <Route path="/channels" element={<ChannelsPage />} />
            <Route path="/channels/whatsapp/new" element={<WahaWizardPage />} />
            <Route path="/inbox/:conversationId" element={<ConversationRoute />} />
            <Route path="*" element={<Navigate to="/" />} />
          </Route>
        </Routes>
      </BrowserRouter>
    </QueryClientProvider>
  )
}
