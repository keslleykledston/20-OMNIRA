// Cliente de exemplo da API externa do OMNIRA (contrato: omnira-external-v1.yaml). Sem dependências: só `fetch` (Node 18+ ou navegador).
//
// O que ele mostra, na ordem em que um adaptador precisa:
//   1. Autenticação e erros (RFC 9457): `OmniraError` com `status` e `code`.
//   2. Comandos com `Idempotency-Key` (uma chave por INTENÇÃO, reaproveitada em cada nova tentativa) e `If-Match` (a versão de controle lida).
//   3. Anexo: subir o arquivo e depois enviar com `attachment_id`.
//   4. O laço de consulta SEM webhook: checkpoint -> foto atual -> eventos a cada poucos segundos, sem repetir fato, com 410 = refazer.
//
// Não é produto nem biblioteca: copie o que servir. Os testes (omnira-client.test.ts) usam um `fetch` falso e mostram cada regra funcionando.

export class OmniraError extends Error {
  constructor(
    public status: number,
    public code: string,
    public detail: string,
    /** A versão de controle ATUAL que a recusa trouxe (cabeçalho ETag), quando trouxe: "control-N". */
    public etag?: string,
    public retryAfterSeconds?: number,
  ) {
    super(`${status} ${code}: ${detail}`)
  }
  /** 412/428: a versão que você tinha está velha. Releia a conversa (GET) e decida de novo; não repita às cegas. */
  get stale() {
    return this.status === 412 || this.status === 428
  }
  /** 409: outro titular, conversa fechada, fora do horário... O `code` diz qual. */
  get conflict() {
    return this.status === 409
  }
  /** 410 no diário: o cursor é mais antigo do que o OMNIRA guarda. Refaça: checkpoint + foto. */
  get resyncRequired() {
    return this.status === 410
  }
}

export interface Control {
  state: 'unassigned' | 'held'
  holder?: { kind: 'tenant_user' | 'hub_user' | 'crm_application' | 'crm_agent_verified'; mine: boolean }
  version: number
}
export interface Conversation {
  id: string
  status: 'open' | 'closed'
  contact_id?: string
  queue_id?: string
  control: Control
  can: { claim?: boolean; reply?: boolean; release?: boolean; transfer?: boolean; finalize?: boolean }
}
export interface Message {
  id: string
  direction: 'inbound' | 'outbound'
  type: string
  body?: string
  status: string
  sender: { kind: 'contact' | 'agent' | 'crm_application' | 'automation'; mine?: boolean }
  media?: { id: string; kind: string; mime: string; size_bytes: number; status: string }
  created_at: string
}
export interface ApiEvent {
  event_id: string
  seq: number
  type: string
  conversation_id?: string
  aggregate_version?: number | null
  origin: { kind: string; mine?: boolean }
  aggregate: { type: string; id: string }
  data: Record<string, unknown>
}
export interface CommandResult {
  conversation_id: string
  status: 'open' | 'closed'
  control: Control
  changed?: boolean
  message_id?: string
  delivery?: 'queued'
  closure_id?: string
}

type Fetch = typeof fetch

export class OmniraClient {
  constructor(
    private baseUrl: string, // https://omnira.devops.k3gsolutions.com.br/api/v1/external
    private token: string, // omn_ext_<id>.<segredo>: guarde em segredo, nunca no navegador
    private http: Fetch = fetch,
    /** Quantas vezes repetir um comando que falhou por rede/5xx/429 (sempre com a MESMA Idempotency-Key). */
    private retries = 3,
    private sleep: (ms: number) => Promise<void> = (ms) => new Promise((r) => setTimeout(r, ms)),
  ) {
    this.baseUrl = baseUrl.replace(/\/+$/, '')
  }

  // ---- leitura ----
  me() {
    return this.get<Record<string, unknown>>('/me')
  }
  queues() {
    return this.get<{ items: { id: string; name: string }[] }>('/queues').then((r) => r.items)
  }
  conversation(id: string) {
    return this.get<Conversation>(`/conversations/${id}`)
  }
  async conversations(status: 'open' | 'closed' | 'all' = 'open'): Promise<Conversation[]> {
    const all: Conversation[] = []
    let cursor = ''
    for (;;) {
      const q = new URLSearchParams({ status, limit: '100', ...(cursor ? { cursor } : {}) })
      const page = await this.get<{ items: Conversation[]; has_more: boolean; next_cursor?: string }>(`/conversations?${q}`)
      all.push(...page.items)
      if (!page.has_more || !page.next_cursor) return all
      cursor = page.next_cursor
    }
  }
  async messages(conversationId: string): Promise<Message[]> {
    const all: Message[] = []
    let cursor = ''
    for (;;) {
      const q = new URLSearchParams({ limit: '200', ...(cursor ? { cursor } : {}) })
      const page = await this.get<{ items: Message[]; has_more: boolean; next_cursor?: string }>(`/conversations/${conversationId}/messages?${q}`)
      all.push(...page.items)
      if (!page.has_more || !page.next_cursor) return all
      cursor = page.next_cursor
    }
  }
  checkpoint() {
    return this.get<{ seq_head: number; min_seq: number }>('/sync/checkpoint')
  }
  async events(after: number): Promise<{ items: ApiEvent[]; next: number; hasMore: boolean }> {
    const page = await this.get<{ items: ApiEvent[]; next_cursor: string; has_more: boolean }>(`/events?after=${after}&limit=200`)
    const next = Number(page.next_cursor)
    return { items: page.items, next: Number.isFinite(next) ? next : after, hasMore: page.has_more }
  }

  // ---- comandos ----
  // `key` identifica a INTENÇÃO ("mandar este texto nesta conversa"). Gere uma vez, guarde com a intenção e reaproveite em cada nova tentativa.
  claim(conversationId: string, key: string, externalAgentId?: string) {
    return this.command('claim', conversationId, key, undefined, externalAgentId ? { external_agent_id: externalAgentId } : undefined)
  }
  release(conversationId: string, key: string, version: number, externalAgentId?: string) {
    return this.command('release', conversationId, key, version, externalAgentId ? { external_agent_id: externalAgentId } : undefined)
  }
  heartbeat(conversationId: string, key: string, version: number) {
    return this.command('heartbeat', conversationId, key, version)
  }
  sendText(conversationId: string, key: string, version: number, text: string, externalAgentId?: string) {
    return this.command('messages', conversationId, key, version, { text, ...(externalAgentId ? { external_agent_id: externalAgentId } : {}) })
  }
  transfer(conversationId: string, key: string, version: number, queueId: string, externalAgentId?: string) {
    return this.command('transfer', conversationId, key, version, { queue_id: queueId, ...(externalAgentId ? { external_agent_id: externalAgentId } : {}) })
  }
  /** `notifyProtocol: false` quando o CRM já manda a própria mensagem de encerramento (evita avisar o cliente duas vezes). */
  finalize(conversationId: string, key: string, version: number, opts: { reason?: string; note?: string; notifyProtocol?: boolean; externalAgentId?: string } = {}) {
    return this.command('finalize', conversationId, key, version, {
      reason: opts.reason ?? 'resolved',
      ...(opts.note ? { note: opts.note } : {}),
      ...(opts.notifyProtocol === undefined ? {} : { notify_protocol: opts.notifyProtocol }),
      ...(opts.externalAgentId ? { external_agent_id: opts.externalAgentId } : {}),
    })
  }

  /** Anexo em dois passos: sobe o arquivo (a instalação precisa SEGURAR a conversa) e envia com o `attachment_id`; o texto vira a legenda. */
  async uploadAttachment(conversationId: string, fileName: string, data: Blob): Promise<{ attachment_id: string; kind: string; mime: string; size_bytes: number; expires_at: string }> {
    const form = new FormData()
    form.append('file', data, fileName)
    const res = await this.http(`${this.baseUrl}/conversations/${conversationId}/attachments`, { method: 'POST', headers: { Authorization: `Bearer ${this.token}` }, body: form })
    return this.parse(res)
  }
  sendAttachment(conversationId: string, key: string, version: number, attachmentId: string, caption?: string) {
    return this.command('messages', conversationId, key, version, { attachment_id: attachmentId, ...(caption ? { text: caption } : {}) })
  }
  async media(mediaId: string): Promise<{ bytes: ArrayBuffer; contentType: string }> {
    const res = await this.http(`${this.baseUrl}/media/${mediaId}`, { headers: { Authorization: `Bearer ${this.token}` } })
    if (!res.ok) await this.fail(res)
    return { bytes: await res.arrayBuffer(), contentType: res.headers.get('Content-Type') ?? '' }
  }

  // ---- por baixo ----
  private async get<T>(path: string): Promise<T> {
    const res = await this.http(`${this.baseUrl}${path}`, { headers: { Authorization: `Bearer ${this.token}`, Accept: 'application/json' } })
    return this.parse<T>(res)
  }

  private async command(op: string, conversationId: string, key: string, version?: number, body?: unknown): Promise<CommandResult & { replay: boolean; etag: string }> {
    const headers: Record<string, string> = { Authorization: `Bearer ${this.token}`, 'Idempotency-Key': key, 'Content-Type': 'application/json' }
    if (version !== undefined) headers['If-Match'] = `"control-${version}"`
    let last: unknown
    for (let attempt = 0; attempt <= this.retries; attempt++) {
      try {
        const res = await this.http(`${this.baseUrl}/conversations/${conversationId}/${op}`, { method: 'POST', headers, body: body === undefined ? undefined : JSON.stringify(body) })
        const out = await this.parse<CommandResult>(res)
        return { ...out, replay: res.headers.get('Idempotent-Replay') === 'true', etag: res.headers.get('ETag') ?? '' }
      } catch (err) {
        last = err
        // 4xx (menos 429 e "em andamento") é recusa: repetir não muda nada. Rede, 5xx e 429: a MESMA chave de novo.
        if (err instanceof OmniraError && err.status < 500 && err.status !== 429 && err.code !== 'request_in_progress') throw err
        const wait = err instanceof OmniraError && err.retryAfterSeconds ? err.retryAfterSeconds * 1000 : (attempt + 1) * 200
        await this.sleep(wait)
      }
    }
    throw last
  }

  private async parse<T>(res: Response): Promise<T> {
    if (!res.ok) await this.fail(res)
    return (await res.json()) as T
  }

  private async fail(res: Response): Promise<never> {
    let p: { code?: string; detail?: string; title?: string } = {}
    try {
      p = await res.json()
    } catch {
      /* corpo sem JSON */
    }
    const retry = Number(res.headers.get('Retry-After'))
    throw new OmniraError(res.status, p.code ?? 'unknown', p.detail ?? p.title ?? res.statusText, res.headers.get('ETag') ?? undefined, Number.isFinite(retry) && retry > 0 ? retry : undefined)
  }
}

/**
 * Mantém a cópia do CRM sincronizada SEM webhook. Use assim:
 *   const feed = new JournalFeed(client, (ev) => meuCrm.aplicar(ev))
 *   await feed.start()        // checkpoint -> foto -> eventos
 *   // a cada 2-3 s:  await feed.poll()
 * Cada `event_id` é entregue UMA vez ao seu callback. O diário é "pelo menos uma vez": o mesmo fato pode chegar de novo (principalmente logo depois do checkpoint).
 * O evento traz só REFERÊNCIAS (ids e contadores): busque o conteúdo com `messages()`/`conversation()`.
 */
export class JournalFeed {
  cursor = 0
  resyncs = 0
  private seen = new Set<string>()

  constructor(
    private client: OmniraClient,
    private onFact: (ev: ApiEvent) => void | Promise<void>,
    /** Chamado na partida e a cada 410: entregue aqui a foto atual (todas as conversas) para o CRM reconstruir o estado dele. */
    private onSnapshot: (conversations: Conversation[]) => void | Promise<void> = () => {},
  ) {}

  /** 1) checkpoint, 2) foto, 3) eventos depois do checkpoint: nada que mude durante a foto se perde. */
  async start(): Promise<void> {
    const cp = await this.client.checkpoint()
    await this.onSnapshot(await this.client.conversations('all'))
    this.cursor = cp.seq_head
    await this.poll()
  }

  /** Lê o diário até o fim a partir do cursor. Um 410 refaz tudo a partir do checkpoint. */
  async poll(): Promise<void> {
    for (;;) {
      let page
      try {
        page = await this.client.events(this.cursor)
      } catch (err) {
        if (err instanceof OmniraError && err.resyncRequired) {
          this.resyncs++
          this.seen.clear()
          return this.start()
        }
        throw err
      }
      for (const ev of page.items) {
        if (this.seen.has(ev.event_id)) continue
        this.seen.add(ev.event_id)
        await this.onFact(ev)
      }
      this.cursor = page.next // avança mesmo quando todos os fatos da janela foram filtrados
      if (!page.hasMore) return
    }
  }
}
