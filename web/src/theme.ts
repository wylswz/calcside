import { useCallback, useEffect, useState } from 'react'

export type ThemeMode = 'system' | 'light' | 'dark'

const KEY = 'calcside:theme'
const listeners = new Set<(mode: ThemeMode) => void>()

function readStored(): ThemeMode {
  try {
    const raw = window.localStorage.getItem(KEY)
    if (raw === 'light' || raw === 'dark' || raw === 'system') return raw
  } catch { /* storage unavailable */ }
  return 'system'
}

export function applyTheme(mode: ThemeMode) {
  const root = document.documentElement
  if (mode === 'system') root.removeAttribute('data-theme')
  else root.setAttribute('data-theme', mode)
}

export function initTheme() {
  applyTheme(readStored())
}

export function setThemeMode(mode: ThemeMode) {
  try {
    if (mode === 'system') window.localStorage.removeItem(KEY)
    else window.localStorage.setItem(KEY, mode)
  } catch { /* storage unavailable */ }
  applyTheme(mode)
  for (const fn of listeners) fn(mode)
}

export function useTheme(): [ThemeMode, (mode: ThemeMode) => void] {
  const [mode, setMode] = useState<ThemeMode>(readStored)
  useEffect(() => {
    listeners.add(setMode)
    return () => { listeners.delete(setMode) }
  }, [])
  const update = useCallback((next: ThemeMode) => setThemeMode(next), [])
  return [mode, update]
}
