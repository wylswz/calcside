import { useMemo } from 'react'
import {
  SchemaNode, patchSchema, toggleRequired, toggleNullable,
} from '../buildDraft'
import { inputCls } from './ui'

// SchemaFields renders a precision-safe JSON schema as an editable
// field tree. Paths are arrays of real JSON-object keys — '__proto__',
// 'constructor' and 'a.b' names stay safe via computed-key copies.
// anyOf/complex unions and non-boolean additionalProperties stay
// read-only with an explicit "edit in JSON" hint — never widened.

const TYPES = ['object', 'array', 'string', 'integer', 'number', 'boolean', 'null'] as const
const MAX_DEPTH = 32

export default function SchemaFields({ schema, onChange }: {
  schema: string
  onChange: (canonical: string) => void
}) {
  const doc = useMemo<SchemaNode | null>(() => {
    try {
      const d = JSON.parse(schema)
      return d != null && typeof d === 'object' && !Array.isArray(d) ? d : null
    } catch { return null }
  }, [schema])
  if (doc == null) {
    return <div className="dim">Schema is not an object; edit JSON.</div>
  }
  const patch = (d: SchemaNode) => onChange(JSON.stringify(d, null, 2))
  return (
    <div className="schema-fields">
      <NodeFields node={doc} root={doc} path={[]} depth={0} onPatch={patch} />
    </div>
  )
}

function label(path: string[], base: string): string {
  return base + (path.length ? ' ' + path.map((p) => JSON.stringify(p)).join('.') : '')
}

function NodeFields({ node, root, path, depth, onPatch }: {
  node: SchemaNode; root: SchemaNode; path: string[]; depth: number
  onPatch: (d: SchemaNode) => void
}) {
  if (depth > MAX_DEPTH) {
    return <div className="dim">Depth limit reached; edit JSON.</div>
  }
  const props = (node.properties ?? {}) as SchemaNode
  const required = new Set(Array.isArray(node.required) ? (node.required as string[]) : [])
  const names = Object.keys(props).sort()
  const ap = node.additionalProperties
  return (
    <div className="schema-node">
      {names.length === 0 && depth === 0 && (
        <div className="dim">No fields yet. Generate from example or edit JSON.</div>
      )}
      {names.map((name) => (
        <FieldRow key={JSON.stringify(path.concat('properties', name))}
          name={name} node={props[name] as SchemaNode}
          required={required.has(name)}
          root={root} path={path.concat('properties', name)}
          parentPath={path} depth={depth} onPatch={onPatch} />
      ))}
      {/* additionalProperties: only a boolean toggle when boolean/absent;
          a schema-valued setting is JSON-only. */}
      {depth === 0 && (
        <label className="dim">
          <input type="checkbox"
            checked={ap !== false}
            disabled={ap !== undefined && typeof ap !== 'boolean'}
            onChange={(e) => onPatch(patchSchema(root, path,
              (n) => ({ ...n, additionalProperties: e.target.checked })))} />
          {ap !== undefined && typeof ap !== 'boolean'
            ? 'additionalProperties is a schema (edit JSON)'
            : ' Allow additional fields'}
        </label>
      )}
    </div>
  )
}

function FieldRow({ name, node, required, root, path, parentPath, depth, onPatch }: {
  name: string; node: SchemaNode; required: boolean
  root: SchemaNode; path: string[]; parentPath: string[]
  depth: number; onPatch: (d: SchemaNode) => void
}) {
  const type = node?.type
  const types = Array.isArray(type) ? (type as string[]) : typeof type === 'string' ? [type] : []
  const isUnion = node?.anyOf != null
  const nullable = types.includes('null')
  const baseTypes = types.filter((t) => t !== 'null')
  const base = baseTypes.length === 1 ? baseTypes[0] : baseTypes.length === 0 ? (types.length === 1 && types[0] === 'null' ? 'null' : '') : 'union'
  const isObj = base === 'object'
  const isArr = base === 'array'
  const hasType = types.length > 0

  return (
    <div className="field-row">
      <div className="field-head">
        <span className="mono">{name}</span>
        {isUnion || base === 'union' ? (
          <span className="dim">Edit union in JSON</span>
        ) : hasType ? (
          <select
            aria-label={label(path, 'Type')}
            className={inputCls}
            value={base}
            onChange={(e) => {
              // Type change edits ONLY `type`; enum/const/nested data
              // and unknown keywords all survive.
              onPatch(patchSchema(root, path, (n) => {
                const next = { ...n }
                const t = e.target.value
                next.type = nullable && t !== 'null' ? [t, 'null'] : t
                return next
              }))
            }}
          >
            {!TYPES.includes(base as any) && <option value={base}>{base || 'Any'}</option>}
            {TYPES.map((t) => <option key={t} value={t}>{t}</option>)}
          </select>
        ) : (
          <span className="dim">Any (edit JSON)</span>
        )}
        <label>
          <input type="checkbox"
            aria-label={label(path, 'Required')}
            checked={required}
            onChange={(e) => onPatch(toggleRequired(root, parentPath, name, e.target.checked))} />
          Required
        </label>
        <label title={hasType ? '' : 'Set a base type first'}>
          <input type="checkbox"
            aria-label={label(path, 'Nullable')}
            checked={nullable}
            disabled={!hasType || baseTypes.length === 0 || base === 'union' || isUnion}
            onChange={(e) => onPatch(toggleNullable(root, path, e.target.checked))} />
          Nullable
        </label>
      </div>
      {isObj && (
        <div className="field-nested">
          <NodeFields node={node} root={root} path={path} depth={depth + 1} onPatch={onPatch} />
        </div>
      )}
      {isArr && node.items != null && typeof node.items === 'object' && !Array.isArray(node.items) && (
        <div className="field-nested">
          <div className="dim">items</div>
          {(node.items as SchemaNode).type === 'object' || Array.isArray((node.items as SchemaNode).type) ? (
            <NodeFields node={node.items as SchemaNode} root={root}
              path={path.concat('items')} depth={depth + 1} onPatch={onPatch} />
          ) : (
            <ItemFields node={node.items as SchemaNode} root={root}
              path={path.concat('items')} onPatch={onPatch} />
          )}
        </div>
      )}
      {(isObj || isArr) && (
        <div className="dim">Advanced constraints (min/max/enum/const): edit JSON.</div>
      )}
      {typeof node?.description === 'string' && (
        <input className={inputCls} value={node.description as string}
          aria-label={label(path, 'Description')}
          onChange={(e) => onPatch(patchSchema(root, path,
            (n) => ({ ...n, description: e.target.value })))} />
      )}
    </div>
  )
}

// ItemFields edits the type of an array's item schema (non-object).
function ItemFields({ node, root, path, onPatch }: {
  node: SchemaNode; root: SchemaNode; path: string[]; onPatch: (d: SchemaNode) => void
}) {
  const type = typeof node.type === 'string' ? node.type : ''
  return (
    <select className={inputCls} value={type}
      aria-label={label(path, 'Type')}
      onChange={(e) => onPatch(patchSchema(root, path, (n) => ({ ...n, type: e.target.value })))}>
      <option value="">—</option>
      {TYPES.map((t) => <option key={t} value={t}>{t}</option>)}
    </select>
  )
}
