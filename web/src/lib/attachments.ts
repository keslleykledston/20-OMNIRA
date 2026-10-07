import axios from 'axios'
import { API_BASE } from './config'
import { authHeaders } from './session'

/** An uploaded file waiting to be sent (ADR-0024): validated, cleaned and cleared by the antivirus on the server. */
export interface UploadedAttachment {
  id: string
  kind: 'image' | 'audio' | 'video' | 'document'
  mime: string
  size_bytes: number
  file_name: string
  expires_at: string
}

/** Same ceiling as the server (16 MiB): checked here only to answer without uploading a file that would be refused. */
export const MAX_ATTACHMENT_BYTES = 16 * 1024 * 1024

/** What the file picker offers. The server decides by CONTENT; this only narrows the picker. */
export const ATTACHMENT_ACCEPT = 'image/jpeg,image/png,image/webp,audio/ogg,audio/mpeg,audio/mp4,audio/amr,video/mp4,application/pdf,text/plain'

const base = (tenantId: string, conversationId: string) => `${API_BASE}/tenants/${tenantId}/inbox/conversations/${conversationId}/attachments`

export async function uploadAttachment(tenantId: string, conversationId: string, file: File, signal?: AbortSignal): Promise<UploadedAttachment> {
  if (file.size > MAX_ATTACHMENT_BYTES) {
    throw Object.assign(new Error('too large'), { response: { status: 413, data: '' } })
  }
  const form = new FormData()
  form.append('file', file, file.name)
  const res = await axios.post(base(tenantId, conversationId), form, { headers: authHeaders(), signal })
  return res.data as UploadedAttachment
}

/** Best effort: the server also expires unsent uploads on its own. */
export async function removeAttachment(tenantId: string, conversationId: string, attachmentId: string): Promise<void> {
  try {
    await axios.delete(`${base(tenantId, conversationId)}/${attachmentId}`, { headers: authHeaders() })
  } catch {
    /* expires on its own */
  }
}

const REASONS: Record<string, string> = {
  archive_not_allowed: 'Arquivos compactados não podem ser enviados.',
  executable_not_allowed: 'Programas e executáveis não podem ser enviados.',
  active_content: 'Este arquivo contém conteúdo ativo e foi bloqueado.',
  polyglot: 'Este arquivo parece disfarçado e foi bloqueado.',
  pdf_active_content: 'Este PDF contém scripts e foi bloqueado.',
  type_not_allowed: 'Tipo de arquivo não permitido. Use imagem (JPG, PNG), áudio, vídeo MP4, PDF ou texto.',
  declared_type_mismatch: 'O conteúdo do arquivo não corresponde ao tipo informado.',
  image_too_large: 'A imagem tem resolução alta demais.',
  image_unreadable: 'Não foi possível ler esta imagem.',
  empty: 'O arquivo está vazio.',
  too_large: 'Arquivo grande demais (limite de 16 MB).',
  unsupported_for_channel: 'Este canal não consegue entregar este tipo de arquivo (no WhatsApp oficial: JPG, PNG, MP4, PDF, TXT e áudio OGG, MP3, M4A, AMR).',
}

/** Turns the plain-text error of the upload / send endpoints into a sentence for the operator. */
export function describeAttachmentError(err: any): string {
  const status = err?.response?.status
  const body = typeof err?.response?.data === 'string' ? err.response.data : ''
  if (status === 413) return REASONS.too_large
  if (status === 422) {
    const m = body.match(/file not accepted: (\w+)/)
    if (m && REASONS[m[1]]) return REASONS[m[1]]
    if (body.includes('antivirus')) return 'O antivírus bloqueou este arquivo.'
    if (body.includes('cannot send media')) return 'Este canal não envia arquivos.'
    if (body.includes('caption')) return 'A legenda aceita até 1024 caracteres, sem caracteres de controle.'
    if (body.includes('attachment is not available')) return 'O anexo expirou ou já foi enviado. Anexe o arquivo de novo.'
    return body || 'Arquivo não aceito.'
  }
  if (status === 503) return 'O antivírus está indisponível. Tente de novo em instantes.'
  if (status === 429) return 'Muitos envios em sequência. Aguarde um instante.'
  if (status === 409) {
    if (body.includes('too many')) return 'Há anexos demais aguardando envio nesta conversa. Envie ou remova algum.'
    if (body.includes('window')) return 'Janela de 24 h da Meta fechada: só mensagem de template até o cliente escrever de novo.'
    if (body.includes('assigned')) return 'Assuma esta conversa antes de enviar arquivos.'
    return 'A conversa mudou, tente novamente.'
  }
  if (status === 403) return 'Você não pode enviar arquivos nesta conversa.'
  if (status === 501) return 'O envio de arquivos não está habilitado neste servidor.'
  return body || 'Não foi possível enviar o arquivo.'
}

export function formatBytes(n: number): string {
  if (n < 1024) return `${n} B`
  if (n < 1024 * 1024) return `${(n / 1024).toFixed(0)} KB`
  return `${(n / 1024 / 1024).toFixed(1)} MB`
}
