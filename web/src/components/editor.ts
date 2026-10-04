import { useMemo } from 'react'
import { useQuery, useQueryClient } from '@tanstack/react-query'
import { autocompletion } from '@codemirror/autocomplete'
import type { CompletionContext as CursorContext } from '@codemirror/autocomplete'
import { python } from '@codemirror/lang-python'
import { StreamLanguage } from '@codemirror/language'
import { api } from '../api'
import type { CompletionContext, EditorMetadata, FileEntry } from '../api'
import { policySymbols, regoCompletions, starlarkCompletions } from './completion'

const regoLanguage = StreamLanguage.define<{ quote: string }>({
  startState: () => ({ quote: '' }),
  token(stream, state) {
    if (!state.quote && stream.eatSpace()) return null
    if (!state.quote && stream.peek() === '#') {
      stream.skipToEnd()
      return 'comment'
    }
    if (!state.quote && (stream.peek() === '"' || stream.peek() === '`')) state.quote = stream.next()!
    if (state.quote) {
      while (!stream.eol()) {
        const c = stream.next()
        if (c === '\\' && state.quote !== '`') stream.next()
        else if (c === state.quote) {
          state.quote = ''
          break
        }
      }
      return 'string'
    }
    if (stream.match(/\b(?:package|import|as|default|else|if|contains|in|some|every|not|with)\b/)) return 'keyword'
    if (stream.match(/\b(?:true|false|null)\b/)) return 'atom'
    if (stream.match(/\b\d+(?:\.\d+)?(?:[eE][+-]?\d+)?\b/)) return 'number'
    if (stream.match(/[A-Za-z_]\w*/)) return 'variableName'
    stream.next()
    return null
  },
  languageData: { commentTokens: { line: '#' } },
})

export function useStarlarkEditor(id: string, live: boolean, busy: boolean) {
  const qc = useQueryClient()
  const { data, error, isFetching, refetch } = useQuery({
    queryKey: ['completions', id],
    queryFn: () => api.get<CompletionContext>(`/api/v1/instances/${id}/completions`),
    enabled: live && !busy,
    staleTime: 30_000,
    refetchOnWindowFocus: false,
    retry: false,
  })
  const extensions = useMemo(() => {
    const loadFiles = !live || busy ? undefined : async (directory: string, context: CursorContext) => {
      context.addEventListener('abort', () => {}, { onDocChange: true })
      await new Promise((resolve) => setTimeout(resolve, 150))
      if (context.aborted) return []
      const result = await qc.fetchQuery({
        queryKey: ['files', id, directory, 'completion'],
        queryFn: () => api.get<{ entries?: FileEntry[] }>(`/api/v1/instances/${id}/files?path=${encodeURIComponent(directory)}&list_only=true`),
        staleTime: 10_000,
        retry: false,
      })
      return result.entries ?? []
    }
    return [python(), autocompletion({ override: [starlarkCompletions(live ? data : undefined, loadFiles)] })]
  }, [data, id, live, busy, qc])
  return { extensions, error, isFetching, refetch, truncated: data?.truncated ?? false }
}

export function useRegoEditor(capability: string, op: string) {
  const { data, error, isFetching } = useQuery({
    queryKey: ['editor-metadata'],
    queryFn: () => api.get<EditorMetadata>('/api/v1/editor/metadata'),
    staleTime: 5 * 60_000,
    retry: false,
  })
  const extensions = useMemo(() => [
    regoLanguage,
    autocompletion({ override: [regoCompletions(data, capability, op)] }),
  ], [data, capability, op])
  const fields = useMemo(() => policySymbols(data, capability, op).filter((s) => s.name.startsWith('input.')), [data, capability, op])
  return { extensions, fields, metadata: data, error, isFetching }
}
