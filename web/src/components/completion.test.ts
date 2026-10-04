import assert from 'node:assert/strict'
import test from 'node:test'
import { CompletionContext } from '@codemirror/autocomplete'
import type { CompletionSource } from '@codemirror/autocomplete'
import { EditorState } from '@codemirror/state'
import { regoCompletions, starlarkCompletions } from './completion.ts'
import type { CompletionContext as InstanceContext, EditorMetadata, FileEntry } from '../api.ts'

const instance: InstanceContext = {
  symbols: [
    { name: 'fs', kind: 'namespace' },
    { name: 'fs.read', kind: 'function', params: ['path'], detail: '(path)', doc: 'Read a file' },
    { name: 'fs.list', kind: 'function', params: ['dir'], detail: '(dir)' },
    { name: 'fs.write', kind: 'function', params: ['path', 'content'], detail: '(path, content)' },
    { name: 'env', kind: 'variable' },
    { name: 'answer', kind: 'variable', detail: 'int' },
    { name: 'len', kind: 'function' },
    { name: 'ext.search.query', kind: 'function', params: ['q'] },
  ],
  env_keys: ['REGION', 'API_HOST'],
  truncated: false,
}
const metadata: EditorMetadata = {
  rego: [
    { name: 'input', kind: 'namespace' },
    { name: 'input.args', kind: 'property' },
    { name: 'input.phase', kind: 'property' },
    { name: 'input.user', kind: 'property' },
    { name: 'input.user.id', kind: 'property' },
    { name: 'input.result.meta', kind: 'property' },
    { name: 'json.unmarshal', kind: 'function', params: ['value'] },
  ],
  capabilities: [{ name: 'fs', ops: [{ name: 'write', params: ['path', 'content'], policy_args: { path: 'string', bytes: 'number' }, result_meta: { bytes: 'number' } }] }],
}

async function complete(source: CompletionSource, text: string, explicit = true) {
  const state = EditorState.create({ doc: text })
  return await source(new CompletionContext(state, text.length, explicit))
}

const star = starlarkCompletions(instance)
const rego = regoCompletions(metadata, 'fs', 'write')

test('members and signatures come from the instance', async () => {
  const result = await complete(star, 'fs.re')
  assert.equal(result?.from, 3)
  assert.equal(result?.options.find((o) => o.label === 'read')?.detail, '(path)')
  assert.ok(!result?.options.some((o) => o.label === 'answer'))
  assert.deepEqual((await complete(star, 'ext.search.'))?.options.map((o) => o.label), ['query'])
})

test('root variables and Starlark keywords exclude Python-only names', async () => {
  const names = (await complete(star, ''))?.options.map((o) => o.label) ?? []
  assert.ok(names.includes('answer') && names.includes('len') && names.includes('def'))
  for (const name of ['open', 'import', 'async', 'await', 'class', 'net']) assert.ok(!names.includes(name))
  assert.equal(await complete(star, '', false), null)
})

test('keyword arguments include the actual parameter names', async () => {
  const result = await complete(star, 'fs.write("a", co')
  assert.ok(result?.options.some((o) => o.label === 'content='))
  assert.ok(!(await complete(star, 'fs.write(path="a", '))?.options.some((o) => o.label === 'path='))
})

test('env keys complete in subscripts and get', async () => {
  for (const text of ['env["RE', "env.get('RE", 'env [ "RE']) {
    const result = await complete(star, text)
    assert.equal(result?.from, text.length - 2)
    assert.ok(result?.options.some((o) => o.label === 'REGION'))
  }
})

test('comments and unrelated strings do not offer completions', async () => {
  for (const text of ['# fs.', 'print("fs.', '"""fs.', "'''fs.", 'x = "escaped \\" fs.']) {
    assert.equal(await complete(star, text), null, text)
  }
  for (const text of ['# input.', '"input.', '`input.']) assert.equal(await complete(rego, text), null, text)
  assert.ok((await complete(star, '# comment\nfs.'))?.options.length)
})

test('file paths load only a directory and escape inserted names', async () => {
  const dirs: string[] = []
  const entries: FileEntry[] = [
    { name: 'a"b.txt', path: '/work/a"b.txt', is_dir: false, size: 0, mtime: 0 },
    { name: 'sub', path: '/work/sub', is_dir: true, size: 0, mtime: 0 },
  ]
  const source = starlarkCompletions(instance, async (dir) => { dirs.push(dir); return entries })
  const result = await complete(source, 'fs.read("/work/a')
  assert.deepEqual(dirs, ['/work'])
  assert.equal(result?.from, 'fs.read("/work/'.length)
  assert.equal(result?.options.find((o) => o.label === 'a"b.txt')?.apply, 'a\\"b.txt')
  assert.equal(result?.options.find((o) => o.label === 'sub/')?.apply, 'sub/')
  const directory = await complete(source, 'fs.list("')
  assert.deepEqual(directory?.options.map((o) => o.label), ['sub/'])
  await complete(source, 'fs.write(path="sub/a')
  assert.equal(dirs.at(-1), '/work/sub')
  const before = dirs.length
  assert.equal(await complete(source, 'print("/work/'), null)
  assert.equal(dirs.length, before)
})

test('unavailable fs and failed directory reads do not break completion', async () => {
  let calls = 0
  const absent = starlarkCompletions({ ...instance, symbols: [] }, async () => { calls++; return [] })
  assert.equal(await complete(absent, 'fs.read("/work/'), null)
  assert.equal(calls, 0)
  const failing = starlarkCompletions(instance, async () => { throw new Error('denied') })
  assert.equal(await complete(failing, 'fs.read("/work/'), null)
})

test('Rego members, operation args and metadata are separate from call parameters', async () => {
  assert.deepEqual((await complete(rego, 'input.user.'))?.options.map((o) => o.label), ['id'])
  const args = (await complete(rego, 'input.args.'))?.options.map((o) => o.label) ?? []
  assert.ok(args.includes('path') && args.includes('bytes') && !args.includes('content'))
  assert.ok((await complete(rego, 'input.result.meta.'))?.options.some((o) => o.label === 'bytes'))
  assert.deepEqual((await complete(rego, 'json.'))?.options.map((o) => o.label), ['unmarshal'])
  assert.equal(await complete(rego, 'unknown.'), null)
})

test('Rego enum strings use the selected capability and operation', async () => {
  assert.deepEqual((await complete(rego, 'input.capability == "'))?.options.map((o) => o.label), ['fs'])
  assert.deepEqual((await complete(rego, 'input.op == "'))?.options.map((o) => o.label), ['write'])
  assert.deepEqual((await complete(rego, 'input.phase == "'))?.options.map((o) => o.label), ['before', 'after'])
})

test('aborted completions do not request directories', async () => {
  let calls = 0
  const source = starlarkCompletions(instance, async () => { calls++; return [] })
  const text = 'fs.read("/work/'
  const context = new CompletionContext(EditorState.create({ doc: text }), text.length, true)
  Object.defineProperty(context, 'aborted', { get: () => true })
  assert.equal(await source(context), null)
  assert.equal(calls, 0)
})
