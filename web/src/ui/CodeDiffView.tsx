import { useMemo } from 'react'
import CodeMirror from '@uiw/react-codemirror'
import { python } from '@codemirror/lang-python'
import { json } from '@codemirror/lang-json'
import { unifiedMergeView } from '@codemirror/merge'
import { EditorView } from '@codemirror/view'
import { cmTheme } from '../components/codemirror'

const diffTheme = EditorView.theme({
  '.cm-changedLine': { backgroundColor: 'rgb(var(--live) / 0.10)' },
  '.cm-changedText': { backgroundColor: 'rgb(var(--live) / 0.22)' },
  '.cm-deletedChunk': { backgroundColor: 'rgb(var(--danger) / 0.10)' },
  '.cm-deletedChunk .cm-deletedText': { backgroundColor: 'rgb(var(--danger) / 0.22)' },
  '.cm-deletedChunk .cm-chunkButtons': { display: 'none' },
})

function countDiffLines(before: string, after: string): { added: number; removed: number } {
  const a = before ? before.split('\n') : []
  const b = after ? after.split('\n') : []
  const setA = new Map<string, number>()
  for (const line of a) setA.set(line, (setA.get(line) ?? 0) + 1)
  let common = 0
  for (const line of b) {
    const n = setA.get(line) ?? 0
    if (n > 0) {
      common++
      setA.set(line, n - 1)
    }
  }
  return { added: b.length - common, removed: a.length - common }
}

// CodeDiffView: unified Starlark/JSON/text diff viewer built on
// @codemirror/merge, shared by EP-0018/0026 publish review and
// release history comparison.
export function CodeDiffView({
  title,
  before,
  after,
  language = 'starlark',
  maxHeight = '300px',
}: {
  title?: string
  before: string
  after: string
  language?: 'starlark' | 'json' | 'text'
  maxHeight?: string
}) {
  const stats = useMemo(() => countDiffLines(before, after), [before, after])
  const extensions = useMemo(() => {
    const lang = language === 'starlark' ? [python()] : language === 'json' ? [json()] : []
    return [
      ...lang,
      unifiedMergeView({
        original: before,
        mergeControls: false,
        highlightChanges: true,
        gutter: true,
      }),
      diffTheme,
    ]
  }, [before, language])

  return (
    <div className="overflow-hidden rounded-[3px] border border-line bg-paper">
      {title && (
        <div className="flex items-center justify-between border-b border-line bg-surface px-2.5 py-1 text-xs">
          <span className="truncate font-mono font-medium text-ink">{title}</span>
          <span className="ml-2 flex shrink-0 items-center gap-2 font-mono text-[11px] tabular-nums">
            {stats.added > 0 && <span className="text-live">+{stats.added}</span>}
            {stats.removed > 0 && <span className="text-danger">−{stats.removed}</span>}
          </span>
        </div>
      )}
      <CodeMirror
        value={after}
        readOnly
        editable={false}
        maxHeight={maxHeight}
        extensions={extensions}
        theme={cmTheme}
        basicSetup={{ highlightActiveLine: false, highlightActiveLineGutter: false, foldGutter: false, bracketMatching: false }}
      />
    </div>
  )
}
