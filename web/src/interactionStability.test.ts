// Static checks for docs/interaction-stability.md. S1: hover, press,
// focus, open and selected states are paint-only, so nothing moves under
// the pointer and floating layers stay on their anchors (S2). S4: a
// state-driven label never changes its control's width. These checks keep
// new CSS and components inside the rules; e2e/workspaces.spec.ts measures
// the rendered result.
import { test } from 'node:test'
import assert from 'node:assert/strict'
import { readFileSync, readdirSync } from 'node:fs'
import { join } from 'node:path'
import ts from 'typescript'

// Selectors that only match while the user interacts or a toggle is on.
const stateSelector = /:(hover|active|focus|focus-visible|focus-within|checked|disabled|enabled)\b|\[(open|aria-(expanded|pressed|selected|current|checked|disabled)|data-(state|selected|highlighted|disabled))\b/
// Properties that move or resize the element itself. Showing or hiding
// whole panels (display) is content switching, not control feedback.
const layoutProp = /^(width|height|(min|max)-(width|height)|inline-size|block-size|margin(-.+)?|padding(-.+)?|inset(-.+)?|top|right|bottom|left|border(-(top|right|bottom|left|block|inline)(-(start|end))?)?(-width)?|font|font-(size|weight|family|stretch)|letter-spacing|word-spacing|line-height|gap|row-gap|column-gap|position|flex(-.+)?|grid(-.+)?|content|zoom)$/
// Transforms move the box; on a decorative ::before/::after they are fine.
const transformProp = /^(transform|translate|scale|rotate)$/
const stateVariant = /^(hover|focus|focus-visible|focus-within|active|disabled|enabled|checked|open|group-[a-z-]+|peer-[a-z-]+|aria-[a-z-]+|data-\[[^\]]+\]|data-[a-z-]+)$/
const geometryUtility = /^(translate-[xy]-|scale-|rotate-|skew-[xy]-|transform$|[mp][trblxyse]?-|space-[xy]-|gap-|(min-|max-)?[wh]-|size-|inset-|top-|right-|bottom-|left-|start-|end-|border(-[trblxyse])?(-\d+|-\[\d[^\]]*\])?$|text-(xs|sm|base|lg|[2-9]?xl)$|text-\[\d|font-(thin|extralight|light|normal|medium|semibold|bold|extrabold|black)$|tracking-|leading-|(absolute|relative|fixed|sticky|static)$)/

// A Tailwind class that changes geometry under a state variant
// (hover:-translate-y-px, aria-expanded:font-semibold, ...). With
// inState the class is already inside a state rule, so any geometry counts.
function geometryClass(token: string, inState = false): boolean {
  const parts = token.split(/:(?![^[]*\])/)
  const utility = parts.pop()!.replace(/^!?-?/, '')
  return (inState || parts.some((v) => stateVariant.test(v))) && geometryUtility.test(utility)
}

function splitSelectors(selector: string): string[] {
  const out: string[] = []
  let depth = 0
  let cur = ''
  for (const ch of selector) {
    if (ch === '(' || ch === '[') depth++
    if (ch === ')' || ch === ']') depth--
    if (ch === ',' && depth === 0) { out.push(cur.trim()); cur = '' } else cur += ch
  }
  return [...out, cur.trim()]
}

type CssRule = { selector: string; decls: string[]; line: number; within: string[] }

// cssRules flattens style rules with the at-rules they sit in; keyframe
// steps are skipped.
function cssRules(css: string): CssRule[] {
  const src = css.replace(/\/\*[\s\S]*?\*\//g, (m) => m.replace(/[^\n]/g, ' '))
  const out: CssRule[] = []
  const stack: { prelude: string; line: number }[] = []
  let buf = ''
  let line = 1
  for (const ch of src) {
    if (ch === '\n') line++
    if (ch === '{') { stack.push({ prelude: buf.trim(), line }); buf = ''; continue }
    if (ch !== '}') { buf += ch; continue }
    const rule = stack.pop()!
    const decls = buf.split(';').map((d) => d.trim()).filter(Boolean)
    buf = ''
    if (rule.prelude.startsWith('@') || stack.some((s) => s.prelude.startsWith('@keyframes'))) continue
    out.push({ selector: rule.prelude, decls, line: rule.line, within: stack.map((s) => s.prelude) })
  }
  return out
}

// S1: state rules are paint-only.
function cssViolations(css: string, file = 'inline.css'): string[] {
  const out: string[] = []
  for (const rule of cssRules(css)) {
    for (const selector of splitSelectors(rule.selector)) {
      const state = stateSelector.test(selector)
      const pseudo = /::?(before|after)\b/.test(selector)
      for (const decl of rule.decls) {
        if (decl.startsWith('@apply')) {
          for (const token of decl.slice(6).trim().split(/\s+/)) {
            if (geometryClass(token, state)) out.push(`${file}:${rule.line} ${selector} { @apply ${token} }`)
          }
          continue
        }
        const prop = decl.slice(0, decl.indexOf(':')).trim().toLowerCase()
        if (state && (layoutProp.test(prop) || (!pseudo && transformProp.test(prop)))) out.push(`${file}:${rule.line} ${selector} { ${decl} }`)
      }
    }
  }
  return out
}

const maxMotionMs = 420

// P4: keyframe animations only run when reduced motion is not requested,
// and no animation or transition outlasts maxMotionMs.
function motionViolations(css: string, file = 'inline.css'): string[] {
  const out: string[] = []
  for (const rule of cssRules(css)) {
    for (const decl of rule.decls) {
      const prop = decl.slice(0, decl.indexOf(':')).trim().toLowerCase()
      if (!/^(animation|transition)(-name|-duration)?$/.test(prop)) continue
      const where = `${file}:${rule.line} ${rule.selector} { ${decl} }`
      if (prop.startsWith('animation') && !rule.within.some((w) => /prefers-reduced-motion:\s*no-preference/.test(w))) out.push(`${where} outside reduced-motion guard`)
      for (const [, n, unit] of decl.matchAll(/(\d*\.?\d+)(ms|s)\b/g)) {
        if (Number(n) * (unit === 's' ? 1000 : 1) > maxMotionMs) out.push(`${where} longer than ${maxMotionMs}ms`)
      }
    }
  }
  return out
}

const stringLike = (n: ts.Node) => ts.isStringLiteral(n) || ts.isNoSubstitutionTemplateLiteral(n) || ts.isTemplateExpression(n)
const unwrap = (n: ts.Expression): ts.Expression => ts.isParenthesizedExpression(n) ? unwrap(n.expression) : n

const requestStatus = new Set(['isPending', 'isFetching', 'isLoading', 'isRefetching'])

const destructive = /^\s*(delete|remove|revoke|leave|end|discard|destroy|erase)\b/i
const windowDialogs = new Set(['alert', 'confirm', 'prompt', 'window.alert', 'window.confirm', 'window.prompt'])

// TSX checks, each tagged with its invariant:
// S1 geometry classes under state variants;
// S4 button labels that are a text ternary on component state or request
//    status (Copy/Copied, Show/Hide, Save/Saving…) instead of SwapLabel;
//    labels fixed for the control's lifetime (per row, per page) are fine;
// P1 a handler that fires a mutation on a control not disabled while it is
//    pending;
// P2 browser dialogs (alert/confirm/prompt) instead of the console's own;
// P4 keyframe animation classes not limited to motion-safe;
// F2 a destructive control (Delete, Remove, Revoke, Leave, End…) acting
//    directly instead of opening Confirm.
function tsxViolations(code: string, file = 'inline.tsx'): string[] {
  const sf = ts.createSourceFile(file, code, ts.ScriptTarget.Latest, true, ts.ScriptKind.TSX)
  const out: string[] = []
  const at = (n: ts.Node) => `${file}:${sf.getLineAndCharacterOfPosition(n.getStart()).line + 1}`
  const state = new Set<string>()
  const collect = (n: ts.Node) => {
    if (ts.isVariableDeclaration(n) && ts.isArrayBindingPattern(n.name) && n.initializer && ts.isCallExpression(n.initializer)
      && /\buse(State|Reducer)$/.test(n.initializer.expression.getText())) {
      const first = n.name.elements[0]
      if (first && ts.isBindingElement(first) && ts.isIdentifier(first.name)) state.add(first.name.text)
    }
    ts.forEachChild(n, collect)
  }
  collect(sf)
  const interactive = (n: ts.Node): boolean =>
    (ts.isIdentifier(n) && state.has(n.text)) || (ts.isPropertyAccessExpression(n) && requestStatus.has(n.name.text))
    || (ts.forEachChild(n, (c) => interactive(c) || undefined) ?? false)
  const strings = (n: ts.Node): string[] => stringLike(n) && !ts.isTemplateExpression(n) ? [(n as ts.StringLiteral).text]
    : ts.isJsxText(n) ? [n.text] : n.getChildren(sf).flatMap(strings)
  const attr = (el: ts.JsxOpeningLikeElement, name: string) =>
    el.attributes.properties.find((a): a is ts.JsxAttribute => ts.isJsxAttribute(a) && a.name.getText() === name)?.initializer?.getText() ?? ''
  const visit = (n: ts.Node) => {
    if (ts.isStringLiteral(n) || ts.isNoSubstitutionTemplateLiteral(n) || ts.isTemplateHead(n) || ts.isTemplateMiddle(n) || ts.isTemplateTail(n)) {
      for (const token of n.text.split(/\s+/).filter(Boolean)) {
        if (geometryClass(token)) out.push(`${at(n)} S1 class ${token}`)
        const parts = token.split(/:(?![^[]*\])/)
        const utility = parts.pop()!
        if (utility.startsWith('animate-') && utility !== 'animate-none' && !parts.includes('motion-safe')) out.push(`${at(n)} P4 class ${token} (use motion-safe:)`)
      }
    }
    if (ts.isCallExpression(n) && windowDialogs.has(n.expression.getText())) out.push(`${at(n)} P2 ${n.expression.getText()}() (use Modal/Confirm)`)
    if (ts.isJsxOpeningElement(n) || ts.isJsxSelfClosingElement(n)) {
      const handler = attr(n, 'onClick') + ' ' + attr(n, 'onSelect')
      for (const [, name] of handler.matchAll(/(\w+)\.mutate(?:Async)?\(/g)) {
        if (!attr(n, 'disabled').includes(`${name}.isPending`)) out.push(`${at(n)} P1 ${name}.mutate without disabled={… ${name}.isPending}`)
      }
      const label = ts.isJsxOpeningElement(n) ? strings(n.parent) : []
      if (label.some((t) => destructive.test(t)) && /\.mutate(Async)?\(|\bapi\.del\(/.test(handler)) out.push(`${at(n)} F2 destructive control acts directly (use Confirm)`)
    }
    if (ts.isJsxElement(n) && ['Button', 'button'].includes(n.openingElement.tagName.getText())) {
      for (const child of n.children) {
        if (!ts.isJsxExpression(child) || !child.expression) continue
        const e = unwrap(child.expression)
        if (ts.isConditionalExpression(e) && stringLike(unwrap(e.whenTrue)) && stringLike(unwrap(e.whenFalse)) && interactive(e.condition)) {
          out.push(`${at(child)} S4 label ${child.getText()} (use SwapLabel)`)
        }
      }
    }
    ts.forEachChild(n, visit)
  }
  visit(sf)
  return out
}

const root = new URL('.', import.meta.url).pathname
const sources = (ext: string) => readdirSync(root, { recursive: true, encoding: 'utf8' })
  .filter((f) => f.endsWith(ext))
  .map((f) => ({ file: f, code: readFileSync(join(root, f), 'utf8') }))

test('the checks catch what they guard against', () => {
  // S1
  assert.equal(cssViolations('.a:hover { transform: translateY(-1px); box-shadow: none }').length, 1)
  assert.equal(cssViolations('@media (x) { .a, .b[aria-expanded="true"] { padding: 2px } }').length, 1)
  assert.equal(cssViolations('.tab[aria-selected="true"] { border: 1px solid red; font-weight: 600 }').length, 2)
  assert.equal(cssViolations('.t[aria-pressed="true"] { @apply border-b-2 text-accent }').length, 1)
  assert.equal(cssViolations('.x { @apply hover:scale-105 hover:text-ink }').length, 1)
  assert.deepEqual(cssViolations(
    '.a:hover { color: red; background: blue; box-shadow: 0 0 1px; outline-offset: 2px } ' +
    'd[open] > s::before { transform: rotate(90deg) } p[data-state="inactive"] { display: none } ' +
    '@keyframes k { from { transform: scale(0.9) } } .m[data-state="open"] { animation: k 1s; transform-origin: top }'), [])
  assert.equal(tsxViolations('const a = <div className="hover:-translate-y-px aria-expanded:font-semibold hover:text-ink px-2" />').length, 2)
  // P4
  assert.equal(motionViolations('.m[data-state="open"] { animation: k 300ms }').length, 1)
  assert.equal(motionViolations('@media (prefers-reduced-motion: no-preference) { .m { animation: k 0.6s } } .b { transition: color 500ms }').length, 2)
  assert.deepEqual(motionViolations('@media (prefers-reduced-motion: no-preference) { .m { animation: k 420ms ease } } .b { transition: color 240ms, box-shadow 0.32s }'), [])
  assert.equal(tsxViolations('const a = <span className="animate-spin" />; const b = <span className="motion-safe:animate-spin" />').length, 1)
  // S4
  assert.equal(tsxViolations('const [busy, setBusy] = useState(false); const a = <Button>{busy ? "Saving…" : "Save"}</Button>').length, 1)
  assert.equal(tsxViolations('const [open, setOpen] = React.useState(false); const a = <button>{(!open ? `Show` : `Hide`)} all</button>').length, 1)
  assert.equal(tsxViolations('const a = <Button disabled={save.isPending}>{save.isPending ? "Saving…" : "Save"}</Button>').length, 1)
  assert.deepEqual(tsxViolations('const [busy, setBusy] = useState(false); const a = <Button><SwapLabel show={busy ? "Saving…" : "Save"} options={["Save", "Saving…"]} /></Button>'), [])
  assert.deepEqual(tsxViolations('function B({ mine }) { return <Button>{mine ? "leave" : "remove"}</Button> }'), [])
  // P1
  assert.equal(tsxViolations('const a = <Button onClick={() => add.mutate()}>Add</Button>').length, 1)
  assert.deepEqual(tsxViolations('const a = <Button disabled={!email || add.isPending} onClick={() => add.mutate()}>Add</Button>'), [])
  // P2
  assert.equal(tsxViolations('if (!window.confirm("End?")) return; alert("x")').length, 2)
  // F2
  assert.equal(tsxViolations('const a = <Button disabled={del.isPending} onClick={() => del.mutate(id)}>Delete</Button>').length, 1)
  assert.equal(tsxViolations('const a = <MenuItem onSelect={() => api.del(u)}>End instance</MenuItem>').length, 1)
  assert.equal(tsxViolations('const a = <Button onClick={() => r.mutate(id)}>{me ? "leave" : "remove"}</Button>').length, 2)
  assert.deepEqual(tsxViolations('const a = <Button onClick={() => setDeleting(s)}>Delete</Button>'), [])
})

test('CSS: state styles are paint-only (S1), motion is short and optional (P4)', () => {
  assert.deepEqual(sources('.css').flatMap(({ file, code }) => [...cssViolations(code, file), ...motionViolations(code, file)]), [])
})

test('components: S1, S4, P1, P2, P4, F2', () => {
  assert.deepEqual(sources('.tsx').flatMap(({ file, code }) => tsxViolations(code, file)), [])
})
