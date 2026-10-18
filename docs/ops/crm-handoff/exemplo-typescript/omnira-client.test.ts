import { describe, expect, it, vi } from 'vitest'
import { JournalFeed, OmniraClient, OmniraError, type ApiEvent } from './omnira-client'

// `fetch` falso: uma fila de respostas por (método + caminho). Mostra cada regra do cliente sem rede.
function fakeFetch(routes: Record<string, Array<{ status?: number; body?: unknown; headers?: Record<string, string> } | Error>>) {
  const calls: Array<{ url: string; init?: RequestInit }> = []
  const f = (async (input: RequestInfo | URL, init?: RequestInit) => {
    const url = String(input)
    calls.push({ url, init })
    const path = url.replace('https://omnira.test/api/v1/external', '')
    const key = `${init?.method ?? 'GET'} ${path.split('?')[0]}`
    const queue = routes[key]
    const next = queue?.shift()
    if (!next) throw new Error(`sem resposta para ${key}`)
    if (next instanceof Error) throw next
    return new Response(JSON.stringify(next.body ?? {}), { status: next.status ?? 200, headers: { 'Content-Type': 'application/json', ...(next.headers ?? {}) } })
  }) as typeof fetch
  return { f, calls }
}
const client = (f: typeof fetch) => new OmniraClient('https://omnira.test/api/v1/external/', 'omn_ext_x.y', f, 3, async () => {})
const ev = (seq: number, id = `e${seq}`): ApiEvent => ({ event_id: id, seq, type: 'message.created.v1', origin: { kind: 'channel' }, aggregate: { type: 'message', id: `m${seq}` }, data: {} })

describe('comandos', () => {
  it('manda a chave e a versão lida, e lê o ETag e o replay', async () => {
    const { f, calls } = fakeFetch({ 'POST /conversations/c1/messages': [{ status: 202, body: { conversation_id: 'c1', status: 'open', control: { state: 'held', version: 8 }, message_id: 'm1', delivery: 'queued' }, headers: { ETag: '"control-8"', 'Idempotent-Replay': 'true' } }] })
    const out = await client(f).sendText('c1', 'chave-0001', 7, 'Olá')
    expect(calls[0].init?.headers).toMatchObject({ Authorization: 'Bearer omn_ext_x.y', 'Idempotency-Key': 'chave-0001', 'If-Match': '"control-7"' })
    expect(JSON.parse(String(calls[0].init?.body))).toEqual({ text: 'Olá' })
    expect(out).toMatchObject({ message_id: 'm1', delivery: 'queued', replay: true, etag: '"control-8"' })
  })

  it('o claim de uma conversa livre não manda If-Match', async () => {
    const { f, calls } = fakeFetch({ 'POST /conversations/c1/claim': [{ body: { conversation_id: 'c1', status: 'open', control: { state: 'held', version: 1 } } }] })
    await client(f).claim('c1', 'chave-claim1', 'atendente-42')
    expect((calls[0].init?.headers as Record<string, string>)['If-Match']).toBeUndefined()
    expect(JSON.parse(String(calls[0].init?.body))).toEqual({ external_agent_id: 'atendente-42' })
  })

  it('repete com a MESMA chave depois de falha de rede, 5xx ou 429, e respeita o Retry-After', async () => {
    const sleeps: number[] = []
    const { f, calls } = fakeFetch({
      'POST /conversations/c1/messages': [new Error('rede caiu'), { status: 503, body: { code: 'unavailable' } }, { status: 429, body: { code: 'rate_limited' }, headers: { 'Retry-After': '2' } }, { status: 202, body: { conversation_id: 'c1', status: 'open', control: { state: 'held', version: 4 } } }],
    })
    const c = new OmniraClient('https://omnira.test/api/v1/external', 't', f, 3, async (ms) => void sleeps.push(ms))
    await c.sendText('c1', 'chave-0002', 3, 'x')
    expect(calls).toHaveLength(4)
    expect(new Set(calls.map((x) => (x.init?.headers as Record<string, string>)['Idempotency-Key']))).toEqual(new Set(['chave-0002']))
    expect(sleeps[2]).toBe(2000)
  })

  it('não repete uma recusa 4xx: 412 e 409 voltam como erro tipado', async () => {
    const { f, calls } = fakeFetch({
      'POST /conversations/c1/messages': [{ status: 412, body: { code: 'stale_control_version', detail: 'velha' }, headers: { ETag: '"control-9"' } }],
      'POST /conversations/c2/messages': [{ status: 409, body: { code: 'held_by_other', detail: 'outro titular' } }],
    })
    const c = client(f)
    const stale = await c.sendText('c1', 'chave-0003', 3, 'x').catch((e) => e)
    expect(stale).toBeInstanceOf(OmniraError)
    expect(stale).toMatchObject({ status: 412, code: 'stale_control_version', etag: '"control-9"', stale: true })
    const held = await c.sendText('c2', 'chave-0004', 3, 'x').catch((e) => e)
    expect(held).toMatchObject({ status: 409, code: 'held_by_other', conflict: true })
    expect(calls).toHaveLength(2)
  })

  it('anexo: sobe o arquivo e envia com o attachment_id, o texto vira legenda; finalize pode calar o aviso de protocolo', async () => {
    const { f, calls } = fakeFetch({
      'POST /conversations/c1/attachments': [{ status: 201, body: { attachment_id: 'a1', kind: 'image', mime: 'image/png', size_bytes: 5, expires_at: '2026-10-19T00:00:00Z' } }],
      'POST /conversations/c1/messages': [{ status: 202, body: { conversation_id: 'c1', status: 'open', control: { state: 'held', version: 5 }, message_id: 'm9', delivery: 'queued' } }],
      'POST /conversations/c1/finalize': [{ body: { conversation_id: 'c1', status: 'closed', control: { state: 'unassigned', version: 6 }, closure_id: 'z' } }],
    })
    const c = client(f)
    const up = await c.uploadAttachment('c1', 'foto.png', new Blob([new Uint8Array([1, 2, 3, 4, 5])], { type: 'image/png' }))
    expect(calls[0].init?.body).toBeInstanceOf(FormData)
    await c.sendAttachment('c1', 'chave-0005', 5, up.attachment_id, 'Segue a foto')
    expect(JSON.parse(String(calls[1].init?.body))).toEqual({ attachment_id: 'a1', text: 'Segue a foto' })
    await c.finalize('c1', 'chave-0006', 5, { notifyProtocol: false })
    expect(JSON.parse(String(calls[2].init?.body))).toEqual({ reason: 'resolved', notify_protocol: false })
  })
})

describe('JournalFeed (consulta sem webhook)', () => {
  it('checkpoint -> foto -> eventos; cada event_id entregue uma vez; o cursor avança', async () => {
    const { f, calls } = fakeFetch({
      'GET /sync/checkpoint': [{ body: { seq_head: 10, min_seq: 1 } }],
      'GET /conversations': [{ body: { items: [{ id: 'c1', status: 'open', control: { state: 'unassigned', version: 0 }, can: {} }], has_more: false } }],
      'GET /events': [
        { body: { items: [ev(11), ev(12)], next_cursor: '12', has_more: true } },
        { body: { items: [ev(12), ev(13)], next_cursor: '13', has_more: false } }, // o 12 chega de novo
        { body: { items: [], next_cursor: '13', has_more: false } },
      ],
    })
    const got: number[] = []
    const snaps: number[] = []
    const feed = new JournalFeed(client(f), (e) => void got.push(e.seq), (cs) => void snaps.push(cs.length))
    await feed.start()
    expect(snaps).toEqual([1])
    expect(got).toEqual([11, 12, 13])
    expect(feed.cursor).toBe(13)
    expect(calls.filter((x) => x.url.includes('/events')).map((x) => new URL(x.url).searchParams.get('after'))).toEqual(['10', '12'])
    await feed.poll()
    expect(got).toEqual([11, 12, 13])
  })

  it('410: refaz a partir do checkpoint e entrega uma nova foto, sem perder o que veio depois', async () => {
    const { f } = fakeFetch({
      'GET /sync/checkpoint': [{ body: { seq_head: 1, min_seq: 1 } }, { body: { seq_head: 500, min_seq: 400 } }],
      'GET /conversations': [{ body: { items: [], has_more: false } }, { body: { items: [{ id: 'c9', status: 'open', control: { state: 'unassigned', version: 0 }, can: {} }], has_more: false } }],
      'GET /events': [
        { body: { items: [], next_cursor: '1', has_more: false } }, // primeira partida
        { status: 410, body: { code: 'resync_required', detail: 'cursor antigo' } },
        { body: { items: [ev(501)], next_cursor: '501', has_more: false } },
      ],
    })
    const got: number[] = []
    const snaps: number[] = []
    const feed = new JournalFeed(client(f), (e) => void got.push(e.seq), (cs) => void snaps.push(cs.length))
    await feed.start()
    await feed.poll()
    expect(feed.resyncs).toBe(1)
    expect(snaps).toEqual([0, 1])
    expect(got).toEqual([501])
    expect(feed.cursor).toBe(501)
  })

  it('um erro que não é 410 não engole o cursor: a próxima consulta recomeça do mesmo ponto', async () => {
    const { f, calls } = fakeFetch({
      'GET /sync/checkpoint': [{ body: { seq_head: 5, min_seq: 1 } }],
      'GET /conversations': [{ body: { items: [], has_more: false } }],
      'GET /events': [{ body: { items: [], next_cursor: '5', has_more: false } }, { status: 500, body: { code: 'internal' } }, { body: { items: [ev(6)], next_cursor: '6', has_more: false } }],
    })
    const got: number[] = []
    const feed = new JournalFeed(client(f), (e) => void got.push(e.seq))
    await feed.start()
    await expect(feed.poll()).rejects.toBeInstanceOf(OmniraError)
    expect(feed.cursor).toBe(5)
    await feed.poll()
    expect(got).toEqual([6])
    expect(calls.filter((x) => x.url.includes('/events')).map((x) => new URL(x.url).searchParams.get('after'))).toEqual(['5', '5', '5'])
  })
})

vi.setConfig({ testTimeout: 5000 })
