import type { QueueMode } from '../../lib/queues'

export const MODE_LABEL: Record<QueueMode, string> = { manual: 'Manual', round_robin: 'Rodízio', least_loaded: 'Menor carga' }

export const MODE_HELP: Record<QueueMode, string> = {
  manual: 'As conversas ficam na fila para quem atende assumir.',
  round_robin: 'O sistema distribui as conversas entre as pessoas disponíveis, uma de cada vez.',
  least_loaded: 'O sistema entrega a conversa a quem tem menos conversas abertas agora. Só funciona com o motor de roteamento ligado neste ambiente.',
}
