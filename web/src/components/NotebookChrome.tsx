import type { ReactNode } from 'react'
import { Button, Icon } from './ui'

// Shared notebook presentation primitives — the instance workbench and
// the application notebook paint the same segmented view toggle,
// between-cell inserter and collapse chevrons without sharing any
// execution state.

export function NotebookViewToggle({ view, onChange }: { view: 'code' | 'graph'; onChange: (view: 'code' | 'graph') => void }) {
  return <div className="seg-filters nb-view-toggle" role="group" aria-label="Notebook view"><button type="button" aria-pressed={view === 'code'} onClick={() => onChange('code')}>Code</button><button type="button" aria-pressed={view === 'graph'} onClick={() => onChange('graph')}>Graph</button></div>
}

export function NotebookInsert({ name, disabled, onInsert }: { name: string; disabled?: boolean; onInsert?: () => void }) {
  return <div className="nb-insert">{onInsert && <Button variant="ghost" size="xs" className="nb-insert-btn" aria-label={`Insert section after ${name}`} disabled={disabled} onClick={onInsert}><Icon name="plus" /><span>Add section</span></Button>}</div>
}

// One naming scheme for auto-created cells in both notebook surfaces.
export function nextNotebookSectionName(taken: ReadonlySet<string>): string {
  let name = 'untitled_section'
  for (let n = 2; taken.has(name); n++) name = `untitled_section_${n}`
  return name
}

export function NotebookAddSection({ disabled, onAdd }: { disabled?: boolean; onAdd: () => void }) {
  return <Button variant="ghost" size="xs" disabled={disabled} onClick={onAdd}><Icon name="plus" />Add section</Button>
}

export function NotebookCollapse({ name, open, onToggle }: { name: string; open: boolean; onToggle: () => void }) {
  return (
    <Button variant="ghost" size="xs" className="nb-icon-btn nb-collapse"
      aria-expanded={open}
      aria-label={`${open ? 'Collapse' : 'Expand'} ${name} code`}
      onClick={onToggle}
    >
      <span className="nb-chevs" aria-hidden>
        <Icon name="chevron" className="nb-chev nb-chev-down" />
        <Icon name="chevron" className="nb-chev nb-chev-right" />
      </span>
    </Button>
  )
}

// Side-rail catalog kinds — the application notebook docks the
// extension, module and policy pickers as parallel left panels.
export type NotebookCatalogKind = 'extensions' | 'modules' | 'policies'
export type NotebookCatalogTargets = Partial<Record<NotebookCatalogKind, HTMLElement | null>>

// Same graph host for both notebooks — view toggling only flips
// `hidden`, the mounted React Flow tree and its viewport survive.
export function NotebookGraphPane({ hidden, children }: { hidden: boolean; children: ReactNode }) {
  return <div className="nb-graph-panel" hidden={hidden}>{children}</div>
}
