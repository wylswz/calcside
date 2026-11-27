import { useMemo } from 'react'
import CodeMirror from '@uiw/react-codemirror'
import { json } from '@codemirror/lang-json'
import { EditorView } from '@codemirror/view'
import { cmTheme } from '../components/codemirror'

export function validateJson(raw: string): string | null {
  const t = raw.trim()
  if (!t) return null
  try {
    JSON.parse(t)
    return null
  } catch (e) {
    return e instanceof Error ? e.message : 'Invalid JSON'
  }
}

export function formatJson(raw: string): { formatted?: string; error?: string } {
  const t = raw.trim()
  if (!t) return { formatted: '' }
  try {
    return { formatted: JSON.stringify(JSON.parse(t), null, 2) }
  } catch (e) {
    return { error: e instanceof Error ? e.message : 'Invalid JSON' }
  }
}

// JsonEditor: CodeMirror 6 JSON editor sharing the console's cmTheme,
// replacing raw <textarea> JSON inputs across Test, Invoke, Schema,
// ResumeForm, and EP-0026 Verification.
export function JsonEditor({
  id,
  ariaLabel,
  value,
  onChange,
  readOnly = false,
  minHeight = '100px',
  maxHeight = '320px',
  placeholder,
  className = '',
}: {
  id?: string
  ariaLabel?: string
  value: string
  onChange?: (next: string) => void
  readOnly?: boolean
  minHeight?: string
  maxHeight?: string
  placeholder?: string
  className?: string
}) {
  const extensions = useMemo(() => {
    const attrs: Record<string, string> = {}
    if (ariaLabel) attrs['aria-label'] = ariaLabel
    if (id) attrs.id = id
    return [
      json(),
      EditorView.lineWrapping,
      ...(Object.keys(attrs).length ? [EditorView.contentAttributes.of(attrs)] : []),
    ]
  }, [ariaLabel, id])

  const err = useMemo(() => validateJson(value), [value])

  return (
    <div className={`flex min-h-0 flex-col ${className}`}>
      <div className="min-h-0 flex-1 overflow-auto border border-line bg-paper">
        <CodeMirror
          value={value}
          onChange={(v) => onChange?.(v)}
          readOnly={readOnly}
          editable={!readOnly}
          minHeight={minHeight}
          maxHeight={maxHeight}
          placeholder={placeholder}
          extensions={extensions}
          theme={cmTheme}
          basicSetup={{ autocompletion: false, foldGutter: true }}
        />
      </div>
      {err && (
        <div role="alert" className="mt-1 font-mono text-[11px] leading-4 text-warn-text">
          JSON syntax: {err}
        </div>
      )}
    </div>
  )
}
