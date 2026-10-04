import type { Completion, CompletionContext, CompletionResult, CompletionSource } from '@codemirror/autocomplete'
import type { CompletionContext as InstanceContext, CompletionSymbol, EditorMetadata, FileEntry } from '../api.ts'

const starKeywords = 'and break continue def elif else for if in lambda load not or pass return while True False None'.split(' ')
const regoKeywords = 'package import as default else if contains in some every not with true false null'.split(' ')

type LoadFiles = (directory: string, context: CompletionContext) => Promise<FileEntry[]>

function cursor(text: string) {
  let code = ''
  let quote = ''
  let start = 0
  let comment = false
  for (let i = 0; i < text.length;) {
    const c = text[i]
    if (comment) {
      comment = c !== '\n'
      code += c === '\n' ? '\n' : ' '
      i++
    } else if (quote) {
      if (c === '\\' && quote !== '`') {
        const count = Math.min(2, text.length - i)
        code += ' '.repeat(count)
        i += count
      } else if (text.startsWith(quote, i)) {
        code += ' '.repeat(quote.length)
        i += quote.length
        quote = ''
      } else {
        code += c === '\n' ? '\n' : ' '
        i++
      }
    } else if (c === '#') {
      comment = true
    } else if (c === '"' || c === "'" || c === '`') {
      quote = c !== '`' && text.startsWith(c.repeat(3), i) ? c.repeat(3) : c
      start = i + quote.length
      code += ' '.repeat(quote.length)
      i += quote.length
    } else {
      code += c
      i++
    }
  }
  return { code, comment, quote, start, before: code.slice(0, start - quote.length), content: text.slice(start) }
}

function escapeString(value: string, quote: string) {
  return value.replace(/\\/g, '\\\\').replace(/\n/g, '\\n').replace(/\r/g, '\\r').replace(/\t/g, '\\t').replaceAll(quote, '\\' + quote)
}

function stringOptions(from: number, options: string[], quote: string): CompletionResult {
  return { from, options: options.map((label) => ({ label, type: 'constant', apply: escapeString(label, quote) })), validFor: /^[^"'`\\\n]*$/ }
}

function parameterOptions(code: string, symbols: CompletionSymbol[]): Completion[] {
  const stack: number[] = []
  for (let i = 0; i < code.length; i++) {
    if ('([{'.includes(code[i])) stack.push(i)
    else if (')]}'.includes(code[i])) stack.pop()
  }
  const open = stack.at(-1)
  if (open === undefined || code[open] !== '(') return []
  const name = code.slice(0, open).match(/[A-Za-z_]\w*(?:\.[A-Za-z_]\w*)*\s*$/)?.[0].trim()
  const symbol = symbols.find((s) => s.name === name && s.kind === 'function')
  if (!symbol?.params) return []
  const args = code.slice(open + 1)
  if (!/(?:^|,)\s*[A-Za-z_]*$/.test(args)) return []
  const used = new Set(Array.from(args.matchAll(/\b([A-Za-z_]\w*)\s*=/g), (m) => m[1]))
  return symbol.params.flatMap((p) => {
    const name = p.replace(/\?$/, '')
    return /^[A-Za-z_]\w*$/.test(name) && !used.has(name)
      ? [{ label: name + '=', type: 'property', detail: symbol.name + (symbol.detail ?? ''), boost: 10 }]
      : []
  })
}

function symbolOptions(context: CompletionContext, code: string, symbols: CompletionSymbol[], keywords: string[], params: boolean): CompletionResult | null {
  const token = code.match(/(?:[A-Za-z_]\w*\.)*[A-Za-z_]\w*$|(?:[A-Za-z_]\w*\.)+$/)?.[0] ?? ''
  if (!token && /[.\w]$/.test(code)) return null
  const dot = token.lastIndexOf('.')
  const prefix = token.slice(0, dot + 1)
  const word = token.slice(dot + 1)
  const options = new Map<string, Completion>()
  for (const symbol of symbols) {
    if (!symbol.name.startsWith(prefix)) continue
    const tail = symbol.name.slice(prefix.length)
    if (!tail) continue
    const [label, member] = tail.split('.')
    if (!options.has(label) || !member) {
      options.set(label, member ? { label, type: 'namespace' } : {
        label, type: symbol.kind, detail: symbol.detail, info: symbol.doc,
      })
    }
  }
  if (!prefix) {
    for (const label of keywords) options.set(label, { label, type: 'keyword' })
    if (params) for (const option of parameterOptions(code, symbols)) options.set(option.label, option)
  }
  if (!context.explicit && !token && !Array.from(options.values()).some((o) => o.boost)) return null
  return options.size ? { from: context.pos - word.length, options: [...options.values()], validFor: /^\w*$/ } : null
}

export function starlarkCompletions(data?: InstanceContext, loadFiles?: LoadFiles): CompletionSource {
  const symbols = data?.symbols ?? []
  return async (context) => {
    if (context.aborted) return null
    const c = cursor(context.state.sliceDoc(0, context.pos))
    if (c.comment) return null
    if (c.quote) {
      if (c.quote.length !== 1 || c.quote === '`' || c.content.includes('\\') || c.content.includes('\n')) return null
      if (/\benv\s*(?:\[\s*|\.\s*get\s*\(\s*)$/.test(c.before)) {
        return stringOptions(c.start, data?.env_keys ?? [], c.quote)
      }
      const call = c.before.match(/\bfs\.(read|write|append|exists|stat|list|walk|mkdir|delete)\s*\(\s*(?:(?:path|dir)\s*=\s*)?$/)
      if (!call || !loadFiles || !symbols.some((s) => s.name === 'fs.' + call[1] && s.kind === 'function')) return null
      const slash = c.content.lastIndexOf('/')
      const parent = c.content.slice(0, slash + 1)
      const directory = parent.startsWith('/') ? parent : '/work/' + parent
      try {
        const entries = await loadFiles(directory.replace(/\/$/, '') || '/', context)
        if (context.aborted) return null
        return {
          from: c.start + slash + 1,
          options: entries.filter((e) => !['list', 'walk', 'mkdir'].includes(call[1]) || e.is_dir).map((entry) => {
            const label = entry.name + (entry.is_dir ? '/' : '')
            return { label, type: entry.is_dir ? 'namespace' : 'text', apply: escapeString(label, c.quote) }
          }),
          validFor: /^[^/\\"'\n]*$/,
        }
      } catch {
        return null
      }
    }
    return symbolOptions(context, c.code, symbols, starKeywords, true)
  }
}

export function policySymbols(metadata?: EditorMetadata, capability = '', op = ''): CompletionSymbol[] {
  const operation = metadata?.capabilities.find((c) => c.name === capability)?.ops?.find((o) => o.name === op)
  return [
    ...(metadata?.rego ?? []),
    ...Object.entries(operation?.policy_args ?? {}).map(([name, detail]) => ({ name: 'input.args.' + name, kind: 'property', detail })),
    ...Object.entries(operation?.result_meta ?? {}).map(([name, detail]) => ({ name: 'input.result.meta.' + name, kind: 'property', detail: detail + ' (after only)' })),
  ]
}

export function regoCompletions(metadata?: EditorMetadata, capability = '', op = ''): CompletionSource {
  const symbols = policySymbols(metadata, capability, op)
  return (context) => {
    const c = cursor(context.state.sliceDoc(0, context.pos))
    if (c.comment) return null
    if (c.quote) {
      if (c.quote !== '"') return null
      const field = c.before.match(/\binput\.(phase|capability|op)\s*(?:==|!=)\s*$/)?.[1]
      if (!field) return null
      const values = field === 'phase' ? ['before', 'after'] : field === 'capability'
        ? metadata?.capabilities.map((c) => c.name ?? '').filter(Boolean) ?? []
        : metadata?.capabilities.find((c) => c.name === capability)?.ops?.map((o) => o.name ?? '').filter(Boolean) ?? []
      return stringOptions(c.start, values, c.quote)
    }
    return symbolOptions(context, c.code, symbols, regoKeywords, false)
  }
}
