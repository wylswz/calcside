// Build-draft helpers for the persistent-instance editor page. Pure —
// no store/API — so the logic is unit-testable. Drafts stay in tab
// session storage scoped by user + workspace + source instance.

export interface SourceFileSel { path: string; size: number | null }
export interface ContractSide { sample: string; schema: string }

export interface BuildDraft {
  name: string
  entrypoint: string
  sources: SourceFileSel[]
  assets: SourceFileSel[]
  input: ContractSide
  output: ContractSide
  directory: string
}

export const DRAFT_VERSION = 1
export const MAX_SOURCES = 64, MAX_FILES = 128
export const MAX_FILE_BYTES = 1 << 20, MAX_SOURCE_BYTES = 1 << 20, MAX_ASSET_BYTES = 8 << 20
export const MAX_TEXT_BYTES = 64 << 10

export function draftKey(userID: string, workspaceID: string, sourceID: string): string {
  // JSON array framing keeps composite keys unambiguous.
  return `build-draft.v${DRAFT_VERSION}:` + JSON.stringify([userID, workspaceID, sourceID])
}

export function emptyDraft(): BuildDraft {
  return {
    name: '', entrypoint: 'main.star', sources: [], assets: [],
    input: { sample: '', schema: '{}' },
    output: { sample: '', schema: '{}' },
    directory: '',
  }
}

// parseSavedDraft is a strict shape guard: a malformed or foreign blob
// must not crash or silently discard — the caller shows a warning.
export function parseSavedDraft(raw: string): BuildDraft | null {
  let d: any
  try { d = JSON.parse(raw) } catch { return null }
  const side = (s: any) => s != null && typeof s === 'object' &&
    typeof s.sample === 'string' && typeof s.schema === 'string'
  const files = (f: any): f is SourceFileSel[] => Array.isArray(f) && f.every(
    (x) => typeof x?.path === 'string' && (x.size === null || typeof x.size === 'number'))
  if (d == null || typeof d !== 'object' ||
    typeof d.name !== 'string' || typeof d.entrypoint !== 'string' ||
    typeof d.directory !== 'string' ||
    !files(d.sources) || !files(d.assets) || !side(d.input) || !side(d.output)) {
    return null
  }
  return d as BuildDraft
}

// portablePath maps a listing path to the portable relative form the
// release expects: exact /work/ prefix, no backslash/control/./..
// /empty segments. Anything else is rejected outright.
export function portablePath(workPath: string): string | null {
  if (!workPath.startsWith('/work/')) return null
  if (workPath.includes('\\')) return null
  for (const c of workPath) { const n = c.charCodeAt(0); if (n < 32 || n === 127) return null }
  const rel = workPath.slice(6)
  if (rel === '') return null
  for (const seg of rel.split('/')) {
    if (seg === '' || seg === '.' || seg === '..') return null
  }
  return rel
}

export interface FileEntry { name: string; path: string; is_dir: boolean; size: number; mtime: string }
export type FileRole = 'source' | 'asset' | 'none'

// applyPick moves one file into exactly one role — a file is never in
// both lists, and only .star files may be sources. Size 0 is a real
// size; null means "checked at publish".
export function applyPick(draft: BuildDraft, path: string, size: number | null, role: FileRole): BuildDraft {
  const clean: BuildDraft = {
    ...draft,
    sources: draft.sources.filter((f) => f.path !== path),
    assets: draft.assets.filter((f) => f.path !== path),
  }
  if (role === 'source' && path.endsWith('.star')) clean.sources = [...clean.sources, { path, size }]
  if (role === 'asset') clean.assets = [...clean.assets, { path, size }]
  return clean
}

export interface LimitsErr { field: string; message: string }

export function checkLimits(draft: BuildDraft): LimitsErr[] {
  const errs: LimitsErr[] = []
  if (draft.sources.length > MAX_SOURCES) errs.push({ field: 'sources', message: `over ${MAX_SOURCES} sources` })
  if (draft.sources.length + draft.assets.length > MAX_FILES) errs.push({ field: 'files', message: `over ${MAX_FILES} files` })
  const seen = new Set<string>()
  const check = (f: SourceFileSel, bucket: 'source' | 'asset') => {
    if (portablePath('/work/' + f.path) !== f.path) {
      errs.push({ field: f.path, message: 'invalid portable path' })
    }
    if (seen.has(f.path)) errs.push({ field: f.path, message: 'duplicate path' })
    seen.add(f.path)
    if (bucket === 'source' && !f.path.endsWith('.star')) {
      errs.push({ field: f.path, message: 'source must be a .star file' })
    }
    if (f.size != null && (!Number.isFinite(f.size) || f.size < 0)) {
      errs.push({ field: f.path, message: 'invalid file size' })
    }
    if (f.size != null && f.size > MAX_FILE_BYTES) errs.push({ field: f.path, message: 'over 1 MiB per file' })
  }
  let srcBytes = 0, assetBytes = 0
  for (const f of draft.sources) { check(f, 'source'); if (f.size != null) srcBytes += f.size }
  for (const f of draft.assets) { check(f, 'asset'); if (f.size != null) assetBytes += f.size }
  if (srcBytes > MAX_SOURCE_BYTES) errs.push({ field: 'sources', message: 'source aggregate over 1 MiB' })
  if (assetBytes > MAX_ASSET_BYTES) errs.push({ field: 'assets', message: 'asset aggregate over 8 MiB' })
  return errs
}

// normalizeNumber renders a JSON numeric token canonically so exact
// round-trips can be compared: sign + coefficient + 'e' + power.
export function normalizeNumber(token: string): string {
  const m = /^(-?)(\d*)(?:\.(\d*))?(?:[eE]([+-]?\d+))?$/.exec(token)
  if (!m) return ''
  const [, sign, intPart, fracPart = '', expPart = '0'] = m
  let coeff = (intPart + fracPart).replace(/^0+/, '')
  let power = parseInt(expPart, 10) - fracPart.length
  // Strip trailing zeros from the coefficient, folding them into power.
  const trimmed = coeff.replace(/0+$/, '')
  power += coeff.length - trimmed.length
  coeff = trimmed
  if (coeff === '') return '0'
  return (sign === '-' ? '-' : '') + coeff + 'e' + power
}

// jsonPrecise reports whether every numeric literal in raw round-trips
// through Number without changing value — the guard for visual field
// editing. Unsafe schemas must stay in the JSON editor.
export function jsonPrecise(raw: string): boolean {
  let inStr = false, esc = false
  for (let i = 0; i < raw.length; i++) {
    const c = raw[i]
    if (inStr) {
      if (esc) esc = false
      else if (c === '\\') esc = true
      else if (c === '"') inStr = false
      continue
    }
    if (c === '"') { inStr = true; continue }
    if (c === '-' || (c >= '0' && c <= '9')) {
      let j = i
      const start = j
      while (j < raw.length && /[0-9.eE+-]/.test(raw[j])) j++
      const tok = raw.slice(start, j)
      if (tok.length > 256) return false
      const exp = tok.match(/[eE]([+-]?\d+)/)
      if (exp && Math.abs(parseInt(exp[1], 10)) > 1024) return false
      const n = Number(tok)
      if (!Number.isFinite(n)) return false
      if (normalizeNumber(tok) !== normalizeNumber(JSON.stringify(n))) return false
      i = j - 1
    }
  }
  return true
}

// exportDraft serializes the draft for download. Schemas are inserted
// as raw JSON text (never reparsed through JS numbers); metadata is
// JSON.stringify'd — samples, file contents and secrets never leave.
export function exportDraft(
  draft: BuildDraft, sourceInstanceID: string, workspaceID: string,
): string {
  const head = JSON.stringify({
    format: 'calcside.build-draft.v1',
    source_instance_id: sourceInstanceID,
    workspace_id: workspaceID,
    name: draft.name,
    entrypoint: draft.entrypoint,
    sources: draft.sources.map((f) => f.path),
    assets: draft.assets.map((f) => f.path),
  })
  const inner = head.slice(1, -1)
  return `{"input_schema":${draft.input.schema},"output_schema":${draft.output.schema},${inner}}`
}

// --- schema field editing ---

export type SchemaNode = Record<string, unknown>

// patchSchema applies update at path — an array of real JSON-object
// keys (e.g. ['properties','age','items']) — using computed-key copies
// so __proto__ / constructor / 'a.b' stay safe.
export function patchSchema(
  root: SchemaNode, path: string[], update: (node: SchemaNode) => SchemaNode,
): SchemaNode {
  if (path.length === 0) return update(root)
  const [head, ...rest] = path
  const child = Object.prototype.hasOwnProperty.call(root, head)
    ? (root[head] as SchemaNode) : {}
  return {
    ...root,
    [head]: patchSchema(
      child != null && typeof child === 'object' && !Array.isArray(child)
        ? child : {}, rest, update),
  }
}

// toggleRequired adds/removes name in the PARENT's required list only —
// never touches types or sibling fields.
export function toggleRequired(
  root: SchemaNode, parentPath: string[], name: string, required: boolean,
): SchemaNode {
  return patchSchema(root, parentPath, (node) => {
    const cur = Array.isArray(node.required) ? new Set(node.required as string[]) : new Set<string>()
    if (required) cur.add(name); else cur.delete(name)
    return { ...node, required: [...cur].sort() }
  })
}

// toggleNullable appends/removes 'null' in THIS node's type union —
// never touches the parent's required list.
export function toggleNullable(
  root: SchemaNode, nodePath: string[], nullable: boolean,
): SchemaNode {
  return patchSchema(root, nodePath, (node) => {
    const cur = node.type
    let types: string[]
    if (Array.isArray(cur)) types = [...(cur as string[])]
    else if (typeof cur === 'string') types = [cur]
    else types = []
    const has = types.includes('null')
    if (nullable && !has) types = [...types, 'null']
    if (!nullable) types = types.filter((t) => t !== 'null')
    return { ...node, type: types.length === 1 ? types[0] : types }
  })
}
