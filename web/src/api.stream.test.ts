import assert from 'node:assert/strict'
import test from 'node:test'
import { postApplication, StreamProtocolError, ApiError } from './api.ts'

const enc = new TextEncoder()
function sse(...chunks: string[]): ReadableStream<Uint8Array> {
  return new ReadableStream({ start(c) { for (const ch of chunks) c.enqueue(enc.encode(ch)); c.close() } })
}
function bytes(...parts: Uint8Array[]): ReadableStream<Uint8Array> {
  return new ReadableStream({ start(c) { for (const p of parts) c.enqueue(p); c.close() } })
}
function resp(body: ReadableStream<Uint8Array> | string, type = 'text/event-stream', status = 200): Response {
  return new Response(body as any, { status, headers: { 'Content-Type': type } })
}
const prevFetch = globalThis.fetch
function stubFetch(fn: typeof fetch) {
  globalThis.fetch = fn
  test.after(() => { globalThis.fetch = prevFetch })
}
const DONE = '{"result":{"id":"r1","streaming":true,"stream_items":2,"stream_bytes":6}}'
const ERRDONE = '{"result":{"id":"r9","error":{"code":"invalid_output","message":"boom"},"streaming":true,"stream_items":1},"run_id":"run_9","run_status":"failed"}'

test('plain JSON response resolves; onStart never fires', async () => {
  let started = 0
  stubFetch(async () => resp('{"result":{"id":"r1"}}', 'application/json'))
  const r = await postApplication('/api/v1/applications/a/invoke', '{}', { onStart: () => started++ })
  assert.equal((r as any).result.id, 'r1')
  assert.equal(started, 0)
})

test('items verbatim + raw precision; done terminal resolves', async () => {
  const items: string[] = []
  stubFetch(async () => resp(sse(': stream\n\nevent: item\nda', 'ta: {"n":12345678901234567890}\n\nevent: item\ndata: null\n\nevent: done\ndata: ', DONE, '\n\n')))
  const r = await postApplication('/x', '{}', { onItem: (raw) => items.push(raw) })
  assert.deepEqual(items, ['{"n":12345678901234567890}', 'null'])
  assert.equal((r as any).result.stream_items, 2)
})

test('error terminal resolves ApplicationRun preserving metadata', async () => {
  const items: string[] = []
  stubFetch(async () => resp(sse('event: item\ndata: 1\n\nevent: error\ndata: ' + ERRDONE + '\n\n')))
  const r = await postApplication('/x', '{}', { onItem: (raw) => items.push(raw) })
  assert.equal((r as any).result.error.code, 'invalid_output')
  assert.equal((r as any).run_id, 'run_9')
  assert.deepEqual(items, ['1'])
})

test('empty stream: start then done, zero items', async () => {
  let started = 0
  const items: string[] = []
  stubFetch(async () => resp(sse(': stream\n\nevent: done\ndata: {"result":{"id":"e","streaming":true,"stream_items":0}}\n\n')))
  const r = await postApplication('/x', '{}', { onStart: () => started++, onItem: (r) => items.push(r) })
  assert.equal(started, 1)
  assert.equal(items.length, 0)
  assert.equal((r as any).result.id, 'e')
})

test('multibyte split across chunks; BOM stripped; CRLF and bare CR endings', async () => {
  const items: string[] = []
  const b = enc.encode('\uFEFFevent: item\ndata: "中"\r\n\r\nevent: done\ndata: {"result":{"id":"x","streaming":true}}\r\r')
  stubFetch(async () => resp(bytes(b.slice(0, 20), b.slice(20))))
  const r = await postApplication('/x', '{}', { onItem: (r) => items.push(r) })
  assert.deepEqual(items, ['"中"'])
  assert.equal((r as any).result.id, 'x')
})

test('multiline data joins; empty first line preserved', async () => {
  const items: string[] = []
  stubFetch(async () => resp(sse('event: item\ndata:\ndata: 42\n\nevent: done\ndata: {"result":{"id":"x","streaming":true}}\n\n')))
  await postApplication('/x', '{}', { onItem: (r) => items.push(r) })
  assert.deepEqual(items, ['\n42'])
})

test('fatal UTF-8 rejects replacement characters', async () => {
  stubFetch(async () => resp(bytes(enc.encode('event: item\ndata: "'), new Uint8Array([0xff]), enc.encode('"\n\n'))))
  await assert.rejects(() => postApplication('/x', '{}'), (e: any) => e instanceof StreamProtocolError)
})

test('unknown event fields/lines ignored, eventjunk rejected, missing terminal rejected', async () => {
  for (const bad of [
    'event: item\ndata: 1\n\n',
    'event: done\ndata: {}\n\n',
    'eventjunk: done\ndata: {"result":{"id":"x","streaming":true}}\n\n',
    'event: item\ndata: 1\n\nevent: done\ndata: {"result":{"id":"x","streaming":true}}\n\nevent: item\ndata: 2\n\n',
    'event: done\ndata: {"result":{"id":"a","streaming":true}}\n\nevent: done\ndata: {"result":{"id":"b","streaming":true}}\n\n',
    'event: item\ndata: 1',
    'event: done\ndata: {"result":{"id":"x","streaming":true}}\n\n{"trailing":1}',
  ]) {
    stubFetch(async () => resp(sse(bad)))
    await assert.rejects(() => postApplication('/x', '{}'), StreamProtocolError)
  }
})

test('many tiny frames in one huge chunk OK; oversized incomplete frame rejected', async () => {
  const many = 'event: item\ndata: 0\n\n'.repeat(5000) + 'event: done\ndata: {"result":{"id":"x","streaming":true}}\n\n'
  stubFetch(async () => resp(sse(many)))
  const r = await postApplication('/x', '{}')
  assert.equal((r as any).result.id, 'x')
  stubFetch(async () => resp(sse('data: ' + 'x'.repeat(3 << 20))))
  await assert.rejects(() => postApplication('/x', '{}'), StreamProtocolError)
})

test('callback throw cancels reader and propagates', async () => {
  let cancelled = false
  stubFetch(async () => resp(new ReadableStream({
    start(c) { c.enqueue(enc.encode('event: item\ndata: 1\n\nevent: done\ndata: {"result":{"id":"x","streaming":true}}\n\n')) },
    cancel() { cancelled = true },
  })))
  await assert.rejects(() => postApplication('/x', '{}', { onItem: () => { throw new Error('cb') } }), /cb/)
  assert.ok(cancelled)
})

test('aborted signal mid-stream rejects', async () => {
  const ctl = new AbortController()
  stubFetch(async () => resp(new ReadableStream({
    start(c) { c.enqueue(enc.encode('event: item\ndata: 1\n\n')); ctl.abort() },
  }), 'text/event-stream'))
  await assert.rejects(() => postApplication('/x', '{}', { signal: ctl.signal, onItem: () => {} }))
})

test('error terminal with empty id resolves service-level failure', async () => {
  const body = '{"result":{"id":"","error":{"code":"internal","message":"request failed"},"streaming":true,"stream_items":0}}'
  stubFetch(async () => resp(sse(': stream\n\nevent: error\ndata: ' + body + '\n\n')))
  const r = await postApplication('/x', '{}')
  assert.equal((r as any).result.error.code, 'internal')
})

test('service JSON error responses still throw ApiError', async () => {
  stubFetch(async () => resp('{"error":{"code":"forbidden","message":"no"}}', 'application/json', 403))
  await assert.rejects(() => postApplication('/x', '{}'), (e: any) => e instanceof ApiError && e.status === 403 && (e.code as string) === 'forbidden')
})
