// entryFunctions lists the top-level one-argument functions in a
// notebook section: the run/debug entry points of an application
// notebook. Parsed with the Python grammar so defs inside strings,
// nested scopes or comments never count — only `def name(param):`
// (optionally `param=default`) at module top level qualifies.
import { pythonLanguage } from '@codemirror/lang-python'

type SyntaxNode = ReturnType<typeof pythonLanguage.parser.parse>['topNode']

export interface EntryFunction {
  name: string
  /** 1-based line number of the `def` line. */
  line: number
}

const NAME = /^[A-Za-z][A-Za-z0-9_]{0,63}$/
// gaps between signature tokens may only be whitespace or comments —
// a stray `/` (positional-only marker) or other token lands here.
const GAP = /^(?:\s|#[^\n]*)*$/

function children(n: SyntaxNode): SyntaxNode[] {
  const out: SyntaxNode[] = []
  for (let c = n.firstChild; c; c = c.nextSibling) out.push(c)
  return out
}

// oneParam accepts `(` name `)`, `(` name `,` `)` and
// `(` name `=` <one expression> [`,`] `)` — nothing else.
function oneParam(list: SyntaxNode, code: string): boolean {
  const kids = children(list)
  if (kids.length < 2 || kids[0].name !== '(' || kids[kids.length - 1].name !== ')') return false
  for (let i = 0; i + 1 < kids.length; i++) {
    if (!GAP.test(code.slice(kids[i].to, kids[i + 1].from))) return false
  }
  const inner = kids.slice(1, -1).filter((k) => k.name !== 'Comment')
  if (inner.length && inner[inner.length - 1].name === ',') inner.pop()
  if (inner.length === 1) return inner[0].name === 'VariableName'
  return inner.length === 3 && inner[0].name === 'VariableName' && inner[1].name === 'AssignOp'
    && inner[2].name !== ',' && inner[2].name !== '(' && inner[2].name !== ')' && inner[2].name !== 'Comment'
}

export function entryFunctions(code: string): EntryFunction[] {
  const out: EntryFunction[] = []
  const top = pythonLanguage.parser.parse(code).topNode
  for (const def of top.getChildren('FunctionDefinition')) {
    const nameNode = def.getChild('VariableName')
    const params = def.getChild('ParamList')
    const name = nameNode ? code.slice(nameNode.from, nameNode.to) : ''
    if (!nameNode || !params || !NAME.test(name) || !oneParam(params, code)) continue
    out.push({ name, line: code.slice(0, def.from).split('\n').length })
  }
  return out
}
