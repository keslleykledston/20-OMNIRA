/**
 * Plain-Portuguese explanations of the error codes WhatsApp (Meta) reports when it refuses a message it had already
 * accepted. The code stays visible next to the text so support can search it. Unknown codes keep the provider's own title.
 */
const META_CODES: Record<string, string> = {
  '130429': 'Limite de mensagens por segundo atingido. Tente de novo em instantes.',
  '130472': 'A Meta não entrega esta mensagem a este número (teste de plataforma da Meta).',
  '131000': 'Erro interno da Meta. Tente novamente.',
  '131005': 'O token de acesso não tem permissão para enviar. Verifique o token em Canais.',
  '131008': 'A Meta recusou a mensagem por parâmetro ausente ou inválido.',
  '131009': 'A Meta recusou um valor da mensagem como inválido.',
  '131016': 'Serviço da Meta indisponível. Tente novamente.',
  '131021': 'O destinatário é o próprio número de envio.',
  '131026': 'Mensagem não entregável: o número pode não ter WhatsApp, estar com o app desatualizado ou ter bloqueado a empresa.',
  '131030': 'Número de destino fora da lista permitida (conta em modo de teste).',
  '131031': 'A conta do WhatsApp Business está bloqueada pela Meta. Veja o aviso no Gerenciador do WhatsApp.',
  '131037': 'O nome de exibição do número ainda precisa ser aprovado pela Meta.',
  '131042': 'Problema de pagamento na conta do WhatsApp Business: configure uma forma de pagamento na Meta (templates são cobrados).',
  '131045': 'O número de envio não está registrado ou verificado na Meta.',
  '131047': 'Janela de 24 h fechada: só é possível enviar um template aprovado.',
  '131048': 'Envio bloqueado por excesso de denúncias ou spam neste número.',
  '131049': 'A Meta decidiu não entregar esta mensagem para manter uma boa experiência do destinatário. Tente mais tarde.',
  '131051': 'Tipo de mensagem não suportado pela Meta.',
  '131052': 'Não foi possível baixar a mídia enviada pelo cliente.',
  '131053': 'Não foi possível enviar a mídia (formato ou tamanho não aceito).',
  '131056': 'Muitas mensagens para o mesmo destinatário em pouco tempo. Aguarde um pouco.',
  '131057': 'Conta do WhatsApp Business em manutenção. Tente mais tarde.',
  '132000': 'A quantidade de variáveis não bate com a do template aprovado.',
  '132001': 'Template não existe neste idioma ou ainda não foi aprovado.',
  '132005': 'O texto do template ficou grande demais depois de preencher as variáveis.',
  '132007': 'O texto do template viola a política de conteúdo da Meta.',
  '132012': 'Uma variável do template está em formato inválido.',
  '132015': 'O template está pausado pela Meta por baixa qualidade.',
  '132016': 'O template foi desativado pela Meta por baixa qualidade.',
  '133010': 'O número de envio não está registrado na Meta.',
}

export interface FailureExplanation {
  /** Short sentence for the attendant (Portuguese). */
  text: string
  /** The provider's code, when there is one. */
  code?: string
}

/** reason as stored by the server: "provider:131042: Business eligibility payment issue" or a short class like "rejected". */
export function explainFailure(reason: string | undefined): FailureExplanation | null {
  const raw = (reason ?? '').trim()
  if (!raw) return null
  const m = /^(?:provider:)?(\d{5,6}):\s*(.*)$/.exec(raw)
  if (m) {
    const [, code, title] = m
    return { code, text: META_CODES[code] ?? (title ? `Recusada pela Meta: ${title}.` : 'Recusada pela Meta.') }
  }
  const classes: Record<string, string> = {
    rejected: 'A Meta recusou a mensagem.',
    authentication: 'O token de acesso do canal foi recusado. Verifique em Canais.',
    window_closed: 'Janela de 24 h fechada: envie um template aprovado.',
    session_disconnected: 'O canal está desconectado.',
    configuration: 'O canal não está configurado corretamente.',
    channel_not_active: 'O canal não está ativo.',
    rate_limited: 'Limite de envios atingido. Tente de novo em instantes.',
  }
  return { text: classes[raw] ?? 'Não foi possível entregar a mensagem.' }
}
