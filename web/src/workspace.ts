import { useQuery } from '@tanstack/react-query'
import { currentWorkspace, workspaces } from './api'

// useRole is the caller's role in the currently selected workspace.
export function useRole(): string {
  const { data } = useQuery({ queryKey: ['workspaces'], queryFn: workspaces, staleTime: 30_000 })
  const cur = currentWorkspace()
  const ws = (data ?? []).find((w) => w.id === cur) ?? (data ?? []).find((w) => w.personal)
  return ws?.role ?? 'owner'
}
