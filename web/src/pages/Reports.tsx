import { useState } from 'react'
import { useQuery, useMutation } from '@tanstack/react-query'
import { reportsAPI } from '../lib/api'
import { isDevSurface, UnavailableSurface } from '../components/UnavailableSurface'

export default function ReportsPage() {
  const [selectedTemplate, setSelectedTemplate] = useState<string | null>(null)
  const [generatedReport, setGeneratedReport] = useState<any>(null)

  const { data: templatesData } = useQuery({
    queryKey: ['report-templates'],
    queryFn: async () => {
      const res = await reportsAPI.list()
      return res.data
    }
  })

  const generateMutation = useMutation({
    mutationFn: (templateId: string) =>
      reportsAPI.generate(templateId, { period: 'Setembro 2026' }),
    onSuccess: (data) => {
      setGeneratedReport(data.data)
      setSelectedTemplate(null)
    }
  })

  const exportMutation = useMutation({
    mutationFn: (format: 'json' | 'csv') => {
      if (format === 'json') {
        return reportsAPI.exportJSON(generatedReport.id)
      } else {
        return reportsAPI.exportCSV(generatedReport.id)
      }
    },
    onSuccess: (data, format) => {
      const filename = `report-${Date.now()}.${format}`
      const element = document.createElement('a')
      element.setAttribute('href', `data:text/plain;charset=utf-8,${encodeURIComponent(data.data)}`)
      element.setAttribute('download', filename)
      element.style.display = 'none'
      document.body.appendChild(element)
      element.click()
      document.body.removeChild(element)
    }
  })

  const templates = templatesData || []

  // reportsAPI is a frontend fixture (no real report backend) — never
  // present it as real business data outside development.
  if (!isDevSurface()) return <UnavailableSurface title="Relatórios" />

  return (
    <div className="space-y-6">
      <div>
        <h1 className="text-3xl font-bold text-gray-900">Relatórios</h1>
        <p className="text-gray-600 mt-1">Gere e exporte relatórios de desempenho</p>
      </div>

      {!generatedReport ? (
        <>
          <div>
            <h2 className="text-xl font-semibold text-gray-900 mb-4">Templates Disponíveis</h2>
            <div className="grid grid-cols-1 md:grid-cols-2 lg:grid-cols-4 gap-4">
              {templates.map((template) => (
                <div
                  key={template.id}
                  onClick={() => setSelectedTemplate(template.id)}
                  className="p-6 bg-white rounded-lg border border-gray-200 hover:border-blue-500 hover:shadow-lg cursor-pointer transition"
                >
                  <div className="text-4xl mb-4">{template.icon}</div>
                  <h3 className="text-lg font-semibold text-gray-900">{template.name}</h3>
                  <p className="text-sm text-gray-600 mt-2">{template.description}</p>
                  <button
                    onClick={(e) => {
                      e.stopPropagation()
                      generateMutation.mutate(template.id)
                    }}
                    disabled={generateMutation.isPending}
                    className="mt-4 w-full px-4 py-2 bg-blue-600 text-white rounded-lg hover:bg-blue-700 disabled:opacity-50"
                  >
                    {generateMutation.isPending ? 'Gerando...' : 'Gerar'}
                  </button>
                </div>
              ))}
            </div>
          </div>

          {/* Modal de Seleção */}
          {selectedTemplate && !generatedReport && (
            <div className="fixed inset-0 bg-black bg-opacity-50 flex items-center justify-center z-50">
              <div className="bg-white rounded-lg p-6 max-w-md w-full mx-4">
                <h2 className="text-xl font-bold text-gray-900 mb-4">Confirmar Geração</h2>
                <p className="text-gray-600 mb-6">
                  Você está prestes a gerar um relatório para {templates.find(t => t.id === selectedTemplate)?.name}.
                </p>
                <div className="flex gap-3 justify-end">
                  <button
                    onClick={() => setSelectedTemplate(null)}
                    className="px-4 py-2 border border-gray-300 rounded-lg text-gray-700 hover:bg-gray-50"
                  >
                    Cancelar
                  </button>
                  <button
                    onClick={() => generateMutation.mutate(selectedTemplate)}
                    disabled={generateMutation.isPending}
                    className="px-4 py-2 bg-blue-600 text-white rounded-lg hover:bg-blue-700 disabled:opacity-50"
                  >
                    {generateMutation.isPending ? 'Gerando...' : 'Gerar'}
                  </button>
                </div>
              </div>
            </div>
          )}
        </>
      ) : (
        <>
          {/* Relatório Gerado */}
          <div className="bg-white rounded-lg border border-gray-200 shadow-sm p-8">
            <div className="flex items-center justify-between mb-6">
              <div>
                <h2 className="text-2xl font-bold text-gray-900">{generatedReport.data.title}</h2>
                <p className="text-gray-600 mt-1">Período: {generatedReport.data.period}</p>
              </div>
              <div className="text-right">
                <p className="text-sm text-gray-600">Gerado em</p>
                <p className="text-sm font-semibold text-gray-900">
                  {new Date(generatedReport.generated_at).toLocaleDateString('pt-BR')}
                </p>
              </div>
            </div>

            <div className="grid grid-cols-2 md:grid-cols-4 gap-4 mb-8">
              {generatedReport.data.metrics.map((metric: any, idx: number) => (
                <div key={idx} className="p-4 bg-gray-50 rounded-lg">
                  <p className="text-sm text-gray-600">{metric.label}</p>
                  <p className="text-2xl font-bold text-gray-900 mt-1">{metric.value}</p>
                  <p className="text-xs text-green-600 mt-1">{metric.trend}</p>
                </div>
              ))}
            </div>

            {/* Tabela de Resumo */}
            <div className="mb-8">
              <h3 className="text-lg font-semibold text-gray-900 mb-4">Resumo</h3>
              <div className="grid grid-cols-2 md:grid-cols-5 gap-4">
                {Object.entries(generatedReport.data.summary).map(([key, value]: [string, any]) => (
                  <div key={key} className="p-4 bg-blue-50 rounded-lg border border-blue-200">
                    <p className="text-xs font-semibold text-gray-600 uppercase">
                      {key.replace(/_/g, ' ')}
                    </p>
                    <p className="text-xl font-bold text-gray-900 mt-2">{String(value)}</p>
                  </div>
                ))}
              </div>
            </div>

            {/* Ações */}
            <div className="flex gap-3">
              <button
                onClick={() => {
                  setGeneratedReport(null)
                  setSelectedTemplate(null)
                }}
                className="px-4 py-2 border border-gray-300 rounded-lg text-gray-700 hover:bg-gray-50"
              >
                Novo Relatório
              </button>
              <button
                onClick={() => exportMutation.mutate('json')}
                disabled={exportMutation.isPending}
                className="px-4 py-2 bg-green-600 text-white rounded-lg hover:bg-green-700 disabled:opacity-50"
              >
                {exportMutation.isPending ? 'Exportando...' : 'Exportar JSON'}
              </button>
              <button
                onClick={() => exportMutation.mutate('csv')}
                disabled={exportMutation.isPending}
                className="px-4 py-2 bg-green-600 text-white rounded-lg hover:bg-green-700 disabled:opacity-50"
              >
                {exportMutation.isPending ? 'Exportando...' : 'Exportar CSV'}
              </button>
            </div>
          </div>
        </>
      )}
    </div>
  )
}
