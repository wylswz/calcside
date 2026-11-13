import type { components } from './api/schema'

type OpDoc = components['schemas']['OpDoc']
type ParamDoc = components['schemas']['ParamDoc']

// starlarkDefault renders a documented default with Starlark spelling:
// None/True/False, quoted strings, plain numbers — mirrors
// capability.starlarkDefault on the server.
function starlarkDefault(v: unknown): string {
  if (v === null || v === undefined) return 'None'
  if (v === true) return 'True'
  if (v === false) return 'False'
  if (typeof v === 'string') return JSON.stringify(v)
  if (typeof v === 'number') return String(v)
  return JSON.stringify(v)
}

// paramLines renders one param doc (and its dict fields, one level
// deep) as "- `name` (type, optional, default x): doc" lines.
function paramLines(p: ParamDoc, indent: string): string[] {
  let line = `${indent}- \`${p.name}\``
  const meta: string[] = []
  if (p.type) meta.push(p.type)
  if (p.optional) meta.push('optional')
  if ('default' in p) meta.push(`default ${starlarkDefault(p.default)}`)
  if (meta.length > 0) line += ` (${meta.join(', ')})`
  if (p.doc) line += `: ${p.doc}`
  const lines = [line]
  for (const f of p.fields ?? []) lines.push(...paramLines(f, indent + '  '))
  return lines
}

// opParamsDoc renders the Parameters/Returns block (no Example),
// unindented — mirrors capability.OpParamsDoc.
function opParamsDoc(op: OpDoc): string[] {
  const lines: string[] = []
  const docs = op.param_docs ?? []
  if (docs.length > 0) {
    lines.push('Parameters:')
    for (const p of docs) lines.push(...paramLines(p, ''))
  }
  if (op.returns) lines.push(`Returns: ${op.returns}`)
  return lines
}

// formatOpDoc renders an op's documentation as plain text in the same
// layout as the server's capability.OpDetail help output: signature
// line, indented Parameters block (dict fields one level deeper),
// Returns, and Example. `name` is the dotted op path
// (e.g. "ext.charts.line" or "fs.read").
export function formatOpDoc(name: string, op: OpDoc): string {
  let text = `- \`${name}(${(op.params ?? []).join(', ')})\``
  if (op.doc) text += ` — ${op.doc}`
  const parts: string[] = []
  for (const line of opParamsDoc(op)) parts.push(`  ${line}`)
  if (op.example) {
    parts.push('  Example:')
    for (const line of op.example.replace(/\n+$/, '').split('\n')) parts.push(`    ${line}`)
  }
  if (parts.length > 0) text += '\n' + parts.join('\n')
  return text
}
