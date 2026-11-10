import { test } from 'node:test'
import assert from 'node:assert/strict'
import { execFileSync } from 'node:child_process'
import { mkdtempSync, rmSync, writeFileSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join } from 'node:path'
import { applicationSnippets } from './applicationSnippets.ts'

const ORIGIN = 'http://127.0.0.1:8796'
const ID = 'app_ABC123'
const BIG = '  {"name":"world","n":9007199254740993,"s":"quo\\"te \\\\\\\\ \\n"}\n'
const payloads = [
  BIG,
  '{"a":"it\'s $(exit 17) `exit 18`","b":"line\\nbreak","path":"C:\\\\tmp\\\\file"}',
  '{\n  "u": "€ é",\n  "empty": null,\n  "bool": true\n}\n',
]
const TOKEN = 'example-only-key'

test('application IDs are URL-encoded rather than interpolated as paths', () => {
  const s = applicationSnippets(ORIGIN, 'app/a?b#c', BIG)
  const url = `${ORIGIN}/api/v1/applications/app%2Fa%3Fb%23c/invoke`
  for (const text of Object.values(s)) assert.ok(text.includes(url))
})

test('curl passes exact raw JSON as one argument, without shell interpolation', () => {
  for (const input of payloads) {
    const snippet = applicationSnippets(ORIGIN, ID, input).curl
    // Override curl: inspect argv without opening any connection.
    const result = execFileSync('/bin/bash', ['-c',
      `curl() { printf '%s\\0' "$@"; }\n${snippet}`], {
      env: { PATH: process.env.PATH, CALCSIDE_API_KEY: TOKEN },
    }).toString().split('\0')
    assert.equal(result[result.indexOf('--data-raw') + 1], input)
    assert.equal(result[result.indexOf('--request') + 1], 'POST')
    assert.ok(result.includes(`Authorization: Bearer ${TOKEN}`))
    assert.ok(result.includes('Content-Type: application/json'))
  }
})

test('Python sends exact UTF-8 JSON bytes and prints verbatim output_json', () => {
  const dir = mkdtempSync(join(tmpdir(), 'snip-'))
  try {
    for (const input of payloads) {
      const file = join(dir, 'snippet.py')
      writeFileSync(file, applicationSnippets(ORIGIN, ID, input).python)
      const harness = `
import ast, contextlib, io, json, runpy, sys, urllib.request
ast.parse(open(sys.argv[1], encoding="utf-8").read())
captured = {}
def fake_open(req):
    captured.update(body=req.data.decode("utf-8"), method=req.method,
                    headers=dict(req.header_items()), url=req.full_url)
    class FakeResp(io.BytesIO):
        class H:
            def get_content_type(self): return "application/json"
        headers = H()
    return FakeResp(json.dumps({"result": {}, "output_json": '{"n":9007199254740993}'}).encode())
urllib.request.urlopen = fake_open
output = io.StringIO()
with contextlib.redirect_stdout(output):
    runpy.run_path(sys.argv[1])
captured["printed"] = output.getvalue()
print(json.dumps(captured))
`
      const result = JSON.parse(execFileSync('python3', ['-c', harness, file], {
        env: { PATH: process.env.PATH, CALCSIDE_API_KEY: TOKEN },
      }).toString())
      assert.equal(result.body, input)
      assert.equal(result.method, 'POST')
      assert.equal(result.headers.Authorization, `Bearer ${TOKEN}`)
      assert.equal(result.printed, '{"n":9007199254740993}\n')
    }
  } finally {
    rmSync(dir, { recursive: true, force: true })
  }
})

test('JavaScript sends an object body, not a double-encoded JSON string', async () => {
  const AsyncFunction = Object.getPrototypeOf(async function () {}).constructor
  for (const input of payloads) {
    const captured: { request?: RequestInit; printed?: unknown } = {}
    const run = new AsyncFunction('fetch', 'process', 'console',
      applicationSnippets(ORIGIN, ID, input).javascript)
    await run(async (url: string, options: RequestInit) => {
      assert.equal(url, `${ORIGIN}/api/v1/applications/${ID}/invoke`)
      captured.request = options
      return { ok: true, headers: { get: () => 'application/json' }, json: async () => ({ result: {}, output_json: '{"n":9007199254740993}' }) }
    }, { env: { CALCSIDE_API_KEY: TOKEN } }, { log: (value: unknown) => { captured.printed = value } })
    assert.equal(captured.request?.body, input)
    assert.equal(captured.request?.method, 'POST')
    assert.equal((captured.request?.headers as Record<string, string>).Authorization, `Bearer ${TOKEN}`)
    assert.equal(typeof JSON.parse(captured.request?.body as string), 'object')
    assert.equal(captured.printed, '{"n":9007199254740993}')
  }
})

test('JavaScript checks credentials before making an HTTP request', async () => {
  const AsyncFunction = Object.getPrototypeOf(async function () {}).constructor
  const run = new AsyncFunction('fetch', 'process', 'console',
    applicationSnippets(ORIGIN, ID, '{}').javascript)
  await assert.rejects(run(() => { assert.fail('fetch must not run without a key') },
    { env: {} }, { log() {} }), /Set CALCSIDE_API_KEY/)
})

test('Python streams text/event-stream lines verbatim', () => {
  const dir = mkdtempSync(join(tmpdir(), 'snip-sse-'))
  try {
    const file = join(dir, 'snippet.py')
    writeFileSync(file, applicationSnippets(ORIGIN, ID, '{}').python)
    const harness = `
import contextlib, io, runpy, sys, urllib.request
class FakeResp(io.BytesIO):
    class H:
        def get_content_type(self): return "text/event-stream"
    headers = H()
urllib.request.urlopen = lambda req: FakeResp(
    b'event: item\\ndata: {"n":9007199254740993}\\n\\nevent: done\\ndata: {"result":{"id":"r"}}\\n\\n')
output = io.StringIO()
with contextlib.redirect_stdout(output):
    runpy.run_path(sys.argv[1])
print(output.getvalue())
`
    const printed = execFileSync('python3', ['-c', harness, file], {
      env: { PATH: process.env.PATH, CALCSIDE_API_KEY: TOKEN },
    }).toString()
    assert.ok(printed.includes('{"n":9007199254740993}'))
    assert.ok(printed.includes('event: done'))
  } finally {
    rmSync(dir, { recursive: true, force: true })
  }
})

test('JavaScript streams text/event-stream body chunks verbatim', async () => {
  const AsyncFunction = Object.getPrototypeOf(async function () {}).constructor
  const full = 'event: item\ndata: "中 文 x"\n\nevent: done\ndata: {"result":{"id":"r"}}\n\n'
  const enc = new TextEncoder().encode(full)
  const chunks = [enc.slice(0, 22), enc.slice(22, 30), enc.slice(30)]
  const written: string[] = []
  const run = new AsyncFunction('fetch', 'process', 'console',
    applicationSnippets(ORIGIN, ID, '{}').javascript)
  await run(async () => ({
    ok: true,
    headers: { get: () => 'text/event-stream' },
    body: (async function* () { for (const c of chunks) yield c })(),
  }), { env: { CALCSIDE_API_KEY: TOKEN }, stdout: { write: (s: string) => { written.push(s) } } }, { log() {} })
  assert.equal(written.join(''), full)
})
