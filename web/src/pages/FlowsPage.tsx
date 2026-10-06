import { useState } from 'react'
import { LoadingState, PageHeader, PermissionState, Tabs } from '../components/primitives'
import FlowListSection from '../components/flows/FlowListSection'
import TemplateLibrary from '../components/flows/TemplateLibrary'
import RunsSection from '../components/flows/RunsSection'
import { useAccess } from '../lib/useAccess'

type Tab = 'flows' | 'library' | 'runs'

// Automação (ADR-0019): fluxos de atendimento, biblioteca de modelos/packs e histórico de execuções. A visibilidade de cada
// área segue a permissão que o backend exige na rota correspondente.
export default function FlowsPage() {
  const access = useAccess()
  const canView = access.can('flow.view')
  const canCreate = access.can('flow.create')
  const canLibrary = access.can('flow_template.view')
  const canInstall = access.can('flow_template.install')
  const canRuns = access.can('flow_run.view')
  const [tab, setTab] = useState<Tab>('flows')

  if (access.isLoading) {
    return <div className="px-6 py-6 lg:px-8 lg:py-8"><LoadingState message="Carregando…" /></div>
  }
  if (!canView && !canLibrary && !canRuns) {
    return <div className="px-6 py-6 lg:px-8 lg:py-8"><PermissionState message="Você não tem permissão para ver a automação de atendimento." /></div>
  }

  return (
    <div className="space-y-6 px-6 py-6 lg:px-8 lg:py-8">
      <PageHeader title="Automação" description="Fluxos que conversam com o contato antes de uma pessoa assumir." />
      <Tabs<Tab>
        aria-label="Seções da automação"
        items={[
          { id: 'flows', label: 'Fluxos', disabled: !canView },
          { id: 'library', label: 'Modelos e packs', disabled: !canLibrary },
          { id: 'runs', label: 'Execuções', disabled: !canRuns },
        ]}
        value={tab}
        onChange={setTab}
      />
      {tab === 'flows' && canView && <FlowListSection canCreate={canCreate} />}
      {tab === 'library' && canLibrary && <TemplateLibrary canInstall={canInstall} />}
      {tab === 'runs' && canRuns && <RunsSection />}
    </div>
  )
}
