import { Fragment, useCallback, useEffect, useLayoutEffect, useMemo, useRef, useState, type PointerEvent as ReactPointerEvent, type ReactNode } from 'react'
import { useSearchParams } from 'react-router-dom'
import CodeMirror from '@uiw/react-codemirror'
import type { Extension } from '@codemirror/state'
import type { Application, NotebookSectionMeta } from '../api'
import { cmTheme } from './codemirror'
import { NotebookGraph } from './NotebookGraph'
import { NotebookAddSection, NotebookCollapse, NotebookGraphPane, NotebookInsert, NotebookViewToggle, nextNotebookSectionName, type NotebookCatalogKind, type NotebookCatalogTargets } from './NotebookChrome'
import { Button, Confirm, Icon, Menu, MenuItem, MenuSeparator, SwapLabel, inputCls } from './ui'
import './notebook.css'
import './applicationNotebook.css'

type Section = NonNullable<Application['sections']>[number]

const SECTION_ID = /^[a-z][a-z0-9_]{0,47}$/
const MAX_SECTIONS = 64

// Application drafts carry no runtime state — the graph paints
// structure and static entry labels only.
const EMPTY_METAS = new Map<string, NotebookSectionMeta>()

interface Props {
  /** live draft sections (edited copy) */
  sections: Section[]
  /** saved application — supplies notebook_graph/notebook_entries */
  saved: Application
  extensions: Extension[]
  readOnly: boolean
  /** '' while idle; 'run'/'publish'/… while a guarded op is in flight */
  busy: string
  canRun: boolean
  onRun: () => void
  onPublish: () => void
  onChange: (update: (sections: Section[]) => Section[]) => void
  test: ReactNode
  saveStatus: ReactNode
  /** this view is the one on screen — the code scroller can't be
      targeted while its Tabs panel is hidden (C2). */
  visible: boolean
  /** Settings content; the extension/module/policy catalogs dock in the
      side rail — parallel add entries (no in-settings duplication). */
  settings: (catalogTargets: NotebookCatalogTargets, openSettings: () => void, closeSettings: () => void) => ReactNode
}

// entryLabels maps a section ID to its static entry labels from the
// latest save: "Main: main" or "Handler: <name>" (never a section kind).
function entryLabelsOf(saved: Application): Map<string, string> {
  const m = new Map<string, string>()
  for (const s of saved.sections ?? []) {
    const names = saved.notebook_entries?.[s.id] ?? []
    const labels = names.map((n) => (n === 'main' ? 'Main: main' : `Handler: ${n}`))
    if (labels.length) m.set(s.id, labels.join(', '))
  }
  return m
}

// mainLast pins the saved entry-point owner to the end of the rendered
// order — an application always reads top-down into main.
function mainLast(sections: Section[], mainId?: string): Section[] {
  const at = sections.findIndex((s) => s.id === mainId)
  return at < 0 || at === sections.length - 1 ? sections : [...sections.slice(0, at), ...sections.slice(at + 1), sections[at]]
}

interface CardHandlers {
  onPatch: (id: string, code: string) => void
  onMove: (id: string, dir: -1 | 1) => void
  onDelete: (id: string) => void
  onInsertAfter: (id: string) => void
  onRename: (id: string, next: string) => string | null
  onDragDown: (e: ReactPointerEvent<HTMLElement>, id: string) => void
  onDragMove: (e: ReactPointerEvent<HTMLElement>) => void
  onDragUp: (e: ReactPointerEvent<HTMLElement>) => void
  onDragCancel: (e: ReactPointerEvent<HTMLElement>) => void
}

function DragIcon() {
  return (
    <svg aria-hidden="true" viewBox="0 0 10 16" fill="currentColor">
      <circle cx="3" cy="3" r="1.3" /><circle cx="7" cy="3" r="1.3" />
      <circle cx="3" cy="8" r="1.3" /><circle cx="7" cy="8" r="1.3" />
      <circle cx="3" cy="13" r="1.3" /><circle cx="7" cy="13" r="1.3" />
    </svg>
  )
}

function AppSectionCard({ sec, index, count, ids, movable, canDelete, canAdd, extensions, readOnly, entryLabel, dropEdge, focusOnMount, onOpenTest, handlers }: {
  sec: Section
  index: number
  count: number
  ids: ReadonlySet<string>
  movable: boolean
  canDelete: boolean
  canAdd: boolean
  extensions: Extension[]
  readOnly: boolean
  entryLabel?: string
  dropEdge?: 'before' | 'after' | null
  focusOnMount?: () => boolean
  /** Pinned main only: gutter shortcut that opens the Test panel —
      navigation only, never an execution. */
  onOpenTest?: () => void
  handlers: CardHandlers
}) {
  const [open, setOpen] = useState(true)
  const [renaming, setRenaming] = useState(false)
  const [renText, setRenText] = useState('')
  const [renErr, setRenErr] = useState('')
  const startRename = () => { setRenText(sec.id); setRenErr(''); setRenaming(true) }
  const submitRename = () => {
    const id = renText.trim()
    const err = id === sec.id ? ''
      : !SECTION_ID.test(id) ? 'Use lowercase letters, digits and _; start with a letter (max 48 chars).'
      : ids.has(id) ? `Section "${id}" already exists.`
      : handlers.onRename(sec.id, id)
    if (err) { setRenErr(err); return }
    setRenaming(false)
  }
  const cls = 'nb-card' + (dropEdge ? ` appnb-drop-${dropEdge}` : '')
  return (
    <div className={cls} id={`section-${sec.id}`}>
      <div className="nb-gutter" aria-hidden={!movable && !onOpenTest || undefined}>
        {onOpenTest ? (
          <Button variant="ghost" size="xs" className="nb-icon-btn appnb-test-entry"
            aria-label="Open Test main" title="Open Test main" onClick={onOpenTest}>
            <Icon name="play" />
          </Button>
        ) : movable && (
          <Button variant="ghost" size="xs" className="nb-icon-btn appnb-drag"
            aria-label={`Drag section ${sec.id}`}
            onPointerDown={(e) => handlers.onDragDown(e, sec.id)}
            onPointerMove={handlers.onDragMove}
            onPointerUp={handlers.onDragUp}
            onPointerCancel={handlers.onDragCancel}
            onLostPointerCapture={handlers.onDragCancel}
            onKeyDown={(e) => {
              if (e.key === 'ArrowUp') { e.preventDefault(); handlers.onMove(sec.id, -1) }
              else if (e.key === 'ArrowDown') { e.preventDefault(); handlers.onMove(sec.id, 1) }
            }}>
            <DragIcon />
          </Button>
        )}
      </div>
      <div className="nb-body">
        <header className="nb-card-head">
          <span className="nb-ordinal">{index + 1}.</span>
          {renaming ? (
            <form className="appnb-rename-form" onSubmit={(e) => { e.preventDefault(); submitRename() }}>
              <input className={inputCls} value={renText} maxLength={48} autoFocus
                aria-label={`Rename section ${sec.id}`}
                onChange={(e) => { setRenText(e.target.value); setRenErr('') }}
                onKeyDown={(e) => { if (e.key === 'Escape') setRenaming(false) }}
                onBlur={() => setRenaming(false)} />
              {renErr && <p className="appnb-err" role="alert">{renErr}</p>}
            </form>
          ) : (
            <h3 className="nb-name">{sec.id}</h3>
          )}
          {entryLabel && <span className="appnb-entry">{entryLabel}</span>}
          <div className="nb-card-actions">
            <NotebookCollapse name={sec.id} open={open} onToggle={() => setOpen((o) => !o)} />
            {!readOnly && (
              <Menu trigger={
                <Button variant="ghost" size="xs" className="nb-icon-btn" aria-label={`Section ${sec.id} actions`}>
                  <Icon name="more" />
                </Button>
              }>
                <MenuItem onSelect={startRename}>Rename…</MenuItem>
                {movable && <MenuItem disabled={!canAdd} onSelect={() => handlers.onInsertAfter(sec.id)}>Insert section after</MenuItem>}
                <MenuItem disabled={index <= 0 || !movable} onSelect={() => handlers.onMove(sec.id, -1)}>Move up</MenuItem>
                <MenuItem disabled={index >= count - 1 || !movable} onSelect={() => handlers.onMove(sec.id, 1)}>Move down</MenuItem>
                <MenuSeparator />
                <MenuItem tone="danger" disabled={!canDelete} onSelect={() => handlers.onDelete(sec.id)}>Delete…</MenuItem>
              </Menu>
            )}
          </div>
        </header>
        <div className="nb-editor" hidden={!open}>
          <div role="group" aria-label={`Application section ${sec.id}`}>
            <CodeMirror value={sec.code} minHeight="44px" maxHeight="420px"
              extensions={extensions} theme={cmTheme}
              basicSetup={{ autocompletion: false }}
              editable={!readOnly} readOnly={readOnly}
              onChange={(code) => handlers.onPatch(sec.id, code)}
              onCreateEditor={(view) => { if (focusOnMount?.()) view.focus() }} />
          </div>
        </div>
      </div>
    </div>
  )
}

export function ApplicationNotebook({ sections, saved, extensions, readOnly, busy, canRun, onRun, onPublish, onChange, test, saveStatus, settings, visible }: Props) {
  const [params, setParams] = useSearchParams()
  const view = params.get('nbview') === 'graph' ? 'graph' : 'code'
  const selected = params.get('section') ?? ''

  const [settingsOpen, setSettingsOpen] = useState(false)
  const [testOpen, setTestOpen] = useState(false)
  const [rail, setRail] = useState<'sections' | NotebookCatalogKind | null>(
    () => window.matchMedia('(min-width: 740px)').matches ? 'sections' : null)
  const [catalogTargets, setCatalogTargets] = useState<NotebookCatalogTargets>({})
  // Stable ref callbacks — a fresh identity per render would churn the
  // portal targets through null on every commit.
  const catalogRefs = useMemo(() => {
    const bind = (kind: NotebookCatalogKind) =>
      (el: HTMLDivElement | null) => setCatalogTargets((x) => (x[kind] === el ? x : { ...x, [kind]: el }))
    return { extensions: bind('extensions'), modules: bind('modules'), policies: bind('policies') }
  }, [])
  const settingsButton = useRef<HTMLButtonElement | null>(null)
  const sectionsButton = useRef<HTMLButtonElement | null>(null)
  const testHead = useRef<HTMLHeadingElement | null>(null)
  const [removeId, setRemoveId] = useState<string | null>(null)
  // Explicit Settings close returns focus to its toolbar toggle (S3).
  const closeSettings = useCallback(() => {
    setSettingsOpen(false)
    settingsButton.current?.focus({ preventScroll: true })
  }, [])
  // Gutter run on pinned main is navigation only — the Test panel opens,
  // execution still waits for the explicit Test main button.
  const openTest = useCallback(() => {
    setSettingsOpen(false)
    setTestOpen(true)
    requestAnimationFrame(() => testHead.current?.focus({ preventScroll: true }))
  }, [])
  const [dirFilter, setDirFilter] = useState('')
  const codeRef = useRef<HTMLDivElement | null>(null)
  const focusNext = useRef('')
  const drag = useRef<{ id: string; pointerId: number; startY: number } | null>(null)
  const [dragTarget, setDragTarget] = useState<{ id: string; after: boolean } | null>(null)

  const navigate = useCallback((nextView: 'code' | 'graph', section?: string) => {
    setParams((p) => {
      const n = new URLSearchParams(p)
      if (nextView === 'graph') n.set('nbview', 'graph')
      else n.delete('nbview')
      if (section === undefined) return n
      if (section) n.set('section', section)
      else n.delete('section')
      return n
    })
  }, [setParams])

  // Renaming the main owner must keep its lock and label until the save
  // re-derives them — remember the pending rename across that window.
  const [mainRename, setMainRename] = useState<{ from: string; to: string } | null>(null)
  const savedMainId = saved.sections?.find((s) => saved.notebook_entries?.[s.id]?.includes('main'))?.id
  const mainId =
    (mainRename && savedMainId === mainRename.from && sections.some((s) => s.id === mainRename.to)
      ? mainRename.to : undefined)
    ?? sections.find((s) => saved.notebook_entries?.[s.id]?.includes('main'))?.id
    ?? sections.find((s) => s.id === 'main')?.id
  const ordered = useMemo(() => mainLast(sections, mainId), [sections, mainId])
  const orderedIds = useMemo(() => new Set(ordered.map((s) => s.id)), [ordered])

  const entryLabels = useMemo(() => entryLabelsOf(saved), [saved])
  // The renamed main owner keeps its saved label until the next save.
  const labelFor = useCallback((id: string) =>
    entryLabels.get(id)
    ?? (id === mainId && mainRename?.to === id ? entryLabels.get(mainRename.from) : undefined),
  [entryLabels, mainId, mainRename])
  // The graph paints saved structure only — saved names in pinned order.
  const structure = useMemo(() => ({
    names: mainLast(saved.sections ?? [], savedMainId).map((s) => s.id),
    edges: saved.notebook_graph?.edges ?? [],
    diagnostics: saved.notebook_graph?.diagnostics ?? [],
  }), [saved, savedMainId])
  // scrolledTo records the ?section= navigation this mount already
  // performed so unrelated arrivals never re-scroll (S5).
  const scrolledTo = useRef('')
  // Code→Graph/Settings→Code continuity: the pane is hidden behind
  // `hidden` (display:none resets scrollTop), so capture before the
  // switch and restore on reveal — unless a pending pick owns the jump.
  const codeScrollY = useRef<number | null>(null)
  const captureScroll = useCallback(() => {
    if (codeRef.current?.clientHeight) codeScrollY.current = codeRef.current.scrollTop
  }, [])

  const stale = JSON.stringify(sections) !== JSON.stringify(saved.sections ?? [])
  const graphStatus = stale ? 'Saved draft · changes pending' : 'Based on saved draft.'
  // Only saved structure is graph-addressable — a pending ?section=
  // (unsaved-only id, stale deep link) selects nothing rather than
  // pointing at a nonexistent node.
  const graphSelected = structure.names.includes(selected) ? selected : ''
  // A pending main rename maps the saved node name to the live card.
  const openGraphCode = useCallback((name: string) => {
    scrolledTo.current = ''
    const target = name === mainRename?.from ? mainRename.to : name
    navigate('code', target)
  }, [mainRename, navigate])
  const testVisible = testOpen && !settingsOpen && view === 'code'
  // Dock "Configure" opens the workspace — on the narrow layout the
  // rail overlay would cover it, so that one explicit path closes it.
  const openSettings = useCallback(() => {
    captureScroll()
    setSettingsOpen(true)
    if (window.matchMedia('(max-width: 739px)').matches) setRail(null)
  }, [captureScroll])

  const patch = useCallback((id: string, code: string) => {
    onChange((cur) => cur.map((s) => (s.id === id ? { ...s, code } : s)))
  }, [onChange])

  const move = useCallback((id: string, dir: -1 | 1) => {
    if (id === mainId) return
    onChange((cur) => {
      const next = [...mainLast(cur, mainId)]
      const i = next.findIndex((s) => s.id === id)
      const j = i + dir
      if (i < 0 || j < 0 || j >= next.length || next[j].id === mainId) return cur
      const [x] = next.splice(i, 1)
      next.splice(j, 0, x)
      return mainLast(next, mainId)
    })
  }, [onChange, mainId])

  // Rename returns an error string to show inline, or null on commit.
  const rename = useCallback((oldId: string, newId: string): string | null => {
    onChange((cur) => mainLast(
      cur.map((s) => (s.id === oldId ? { ...s, id: newId } : s)),
      oldId === mainId ? newId : mainId))
    if (oldId === mainId && savedMainId) setMainRename({ from: savedMainId, to: newId })
    if (selected === oldId) navigate(view, newId)
    return null
  }, [onChange, mainId, savedMainId, selected, view, navigate])

  // One "active section" (S1/S3): an explicit pick — directory row,
  // graph node, deep link — highlights at once and holds the scroll-spy
  // until the programmatic scroll settles or the user scrolls.
  const [activeNav, setActiveNav] = useState('')
  const spyHold = useRef(0)
  const selfScrollAt = useRef(0)
  const pickSection = useCallback((id: string) => {
    spyHold.current = performance.now() + 600
    setActiveNav(id)
  }, [])
  const scrollToCard = useCallback((id: string) => {
    const pane = codeRef.current
    const card = document.getElementById(`section-${id}`)
    if (!pane || !card || !pane.contains(card) || !pane.clientHeight) return false
    selfScrollAt.current = performance.now()
    pane.scrollTo({
      top: pane.scrollTop + card.getBoundingClientRect().top - pane.getBoundingClientRect().top - 12,
    })
    return true
  }, [])

  // Code|Graph toggle parity with the instance notebook: the capture
  // runs in the click before navigation, the restore in a layout effect
  // once the pane is unhidden — never a smooth scroll across a hard cut.
  const goView = useCallback((nextView: 'code' | 'graph') => {
    if (nextView === 'graph' && view === 'code') captureScroll()
    if (nextView === 'code' && selected && codeScrollY.current !== null) {
      // A plain Code click with a saved position makes a lingering
      // ?section= stale — mark it scrolled so it cannot jump.
      scrolledTo.current = selected
    }
    navigate(nextView)
  }, [view, navigate, selected, captureScroll])

  const wasHidden = useRef(false)
  useLayoutEffect(() => {
    const hidden = settingsOpen || view === 'graph'
    if (wasHidden.current === hidden) return
    wasHidden.current = hidden
    if (hidden || codeScrollY.current === null) return
    const y = codeScrollY.current
    codeScrollY.current = null
    const pendingNav = !!selected && scrolledTo.current !== selected
    if (!pendingNav && codeRef.current) {
      codeRef.current.scrollTop = y
      // Keep the restored position authoritative: the stale ?section=
      // selection must not re-jump once the effect below runs.
      if (selected) scrolledTo.current = selected
    }
  }, [view, settingsOpen, selected])

  // C4: ?section= deep links and explicit picks scroll the target into
  // the pane's own scroller once — the window never moves. A vanished
  // selection (unsaved-only section shown in the graph, deleted card)
  // simply does not scroll.
  useEffect(() => {
    if (!selected) scrolledTo.current = ''
    if (!visible || !selected || view !== 'code' || settingsOpen || scrolledTo.current === selected) return
    if (scrollToCard(selected)) scrolledTo.current = selected
  }, [visible, selected, view, settingsOpen, scrollToCard])

  // ?section= is the explicit selection: it highlights its directory
  // row at once and holds the spy until the programmatic scroll ends.
  useEffect(() => {
    if (selected) pickSection(selected)
    else { spyHold.current = 0; setActiveNav('') }
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [selected])

  // Scroll-spy: one IntersectionObserver rooted at the code pane; the
  // first intersecting card in order highlights its row, paint-only.
  const spyEls = useRef<{ key: string; el: HTMLElement }[]>([])
  const navKey = ordered.map((s) => s.id).join(',')
  useEffect(() => {
    const root = codeRef.current
    if (!root) return
    const els = ordered
      .map((s) => ({ key: s.id, el: document.getElementById(`section-${s.id}`) }))
      .filter((t): t is { key: string; el: HTMLElement } => t.el !== null)
    spyEls.current = els
    if (!els.length) { setActiveNav(''); return }
    const hit = new Set<string>()
    const byEl = new Map(els.map((t) => [t.el, t.key]))
    const io = new IntersectionObserver((entries) => {
      for (const en of entries) {
        const k = byEl.get(en.target as HTMLElement)
        if (!k) continue
        if (en.isIntersecting) hit.add(k)
        else hit.delete(k)
      }
      const next = els.map((t) => t.key).find((k) => hit.has(k))
      if (next !== undefined && performance.now() >= spyHold.current) {
        setActiveNav((prev) => (prev === next ? prev : next))
      }
    }, { root })
    for (const t of els) io.observe(t.el)
    return () => io.disconnect()
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [navKey])

  // A user scroll ends the pick hold and recomputes the topmost card;
  // our own programmatic scroll is ignored for 150ms.
  useEffect(() => {
    const root = codeRef.current
    if (!root) return
    const release = () => { spyHold.current = 0 }
    const onScroll = () => {
      if (performance.now() - selfScrollAt.current <= 150) return
      release()
      const rTop = root.getBoundingClientRect().top
      const next = spyEls.current.find(({ el }) => {
        const r = el.getBoundingClientRect()
        return r.bottom > rTop + 8 && r.top < rTop + root.clientHeight
      })?.key
      if (next !== undefined) setActiveNav((prev) => (prev === next ? prev : next))
    }
    const scrollKeys = new Set(['ArrowUp', 'ArrowDown', 'PageUp', 'PageDown', 'Home', 'End', ' '])
    const onKey = (e: KeyboardEvent) => { if (scrollKeys.has(e.key)) release() }
    root.addEventListener('wheel', release, { passive: true })
    root.addEventListener('touchstart', release, { passive: true })
    root.addEventListener('keydown', onKey)
    root.addEventListener('scroll', onScroll, { passive: true })
    return () => {
      root.removeEventListener('wheel', release)
      root.removeEventListener('touchstart', release)
      root.removeEventListener('keydown', onKey)
      root.removeEventListener('scroll', onScroll)
    }
  }, [])

  const pick = (id: string) => {
    pickSection(id)
    // The narrow-width Sections overlay closes on a pick and focus
    // returns to its rail toggle (C1); catalog panes stay open.
    if (window.matchMedia('(max-width: 739px)').matches && rail === 'sections') {
      setRail(null)
      sectionsButton.current?.focus({ preventScroll: true })
    }
    if (settingsOpen) {
      // Coming from the dock: the URL selection owns the landing scroll.
      setSettingsOpen(false)
      navigate(view, id)
      return
    }
    // A deliberate re-scroll when the row is already the selection.
    if (view === 'code' && selected === id) scrollToCard(id)
    else navigate(view, id)
  }

  // Shared with the instance notebook: auto-name, no prompt; the new
  // cell lands at the top (toolbar) or right after its inserter.
  const addSection = (after?: string) => {
    if (readOnly || sections.length >= MAX_SECTIONS || (after === mainId && after !== undefined)) return
    const id = nextNotebookSectionName(new Set(sections.map((s) => s.id)))
    focusNext.current = id
    onChange((cur) => {
      const index = after === undefined ? 0 : cur.findIndex((s) => s.id === after) + 1
      if (cur.length >= MAX_SECTIONS || cur.some((s) => s.id === id) || (after !== undefined && index === 0)) return cur
      return mainLast([...cur.slice(0, index), { id, code: '' }, ...cur.slice(index)], mainId)
    })
    setSettingsOpen(false)
    navigate('code', id)
  }

  const doRemove = () => {
    if (!removeId || sections.length <= 1) return
    onChange((cur) => cur.filter((s) => s.id !== removeId))
    if (selected === removeId) navigate(view, '')
    setRemoveId(null)
  }

  // Drop targets are the midpoints of the movable cards only — main
  // stays pinned last and can never receive a drop.
  const targetAt = (clientY: number, dragged: string): { id: string; after: boolean } | null => {
    const pane = codeRef.current
    if (!pane) return null
    const cards = [...pane.querySelectorAll<HTMLElement>('.nb-card')]
    const movable = ordered.filter((s) => s.id !== dragged && s.id !== mainId)
    let last: HTMLElement | null = null
    for (const s of movable) {
      const el = cards.find((c) => c.id === `section-${s.id}`)
      if (!el) continue
      const r = el.getBoundingClientRect()
      if (clientY < r.top + r.height / 2) return { id: s.id, after: false }
      last = el
    }
    return last ? { id: last.id.replace(/^section-/, ''), after: true } : null
  }

  const onDragDown = (e: ReactPointerEvent<HTMLElement>, id: string) => {
    if (readOnly || e.button !== 0 || !e.isPrimary || id === mainId) return
    drag.current = { id, pointerId: e.pointerId, startY: e.clientY }
    e.currentTarget.setPointerCapture(e.pointerId)
  }
  const onDragMove = (e: ReactPointerEvent<HTMLElement>) => {
    const d = drag.current
    if (!d || e.pointerId !== d.pointerId) return
    if (!dragTarget && Math.abs(e.clientY - d.startY) < 4) return
    setDragTarget(targetAt(e.clientY, d.id))
  }
  const onDragUp = (e: ReactPointerEvent<HTMLElement>) => {
    const d = drag.current
    if (!d || e.pointerId !== d.pointerId) return
    const target = Math.abs(e.clientY - d.startY) >= 4 || dragTarget
      ? targetAt(e.clientY, d.id) : null
    const dragged = d.id
    drag.current = null
    setDragTarget(null)
    if (!target) return
    onChange((cur) => {
      const next = [...mainLast(cur, mainId)]
      const from = next.findIndex((s) => s.id === dragged)
      if (from < 0 || dragged === mainId) return cur
      const [cell] = next.splice(from, 1)
      const i = next.findIndex((s) => s.id === target.id)
      if (i < 0 || target.id === mainId) return cur
      next.splice(i + (target.after ? 1 : 0), 0, cell)
      return mainLast(next, mainId)
    })
    navigate(view, dragged)
  }
  const onDragCancel = (e: ReactPointerEvent<HTMLElement>) => {
    if (drag.current && e.pointerId !== drag.current.pointerId) return
    drag.current = null
    setDragTarget(null)
  }

  const handlers: CardHandlers = {
    onPatch: patch, onMove: move, onDelete: setRemoveId, onInsertAfter: addSection, onRename: rename,
    onDragDown, onDragMove, onDragUp, onDragCancel,
  }
  const dropEdgeOf = (id: string): 'before' | 'after' | null =>
    dragTarget?.id === id ? (dragTarget.after ? 'after' : 'before') : null

  const dirMatch = (id: string) => !dirFilter.trim() || id.toLowerCase().includes(dirFilter.trim().toLowerCase())

  return (
    <div className="app-notebook-workbench nb">
      <div className="nb-head appnb-head">
        <div className="nb-toolbar">
          <div className="nb-toolbar-actions">
            {!readOnly && (
              <NotebookAddSection disabled={settingsOpen || sections.length >= MAX_SECTIONS}
                onAdd={() => addSection()} />
            )}
            <Button variant="ghost" size="xs" aria-expanded={testVisible}
              onClick={() => { setSettingsOpen(false); if (view === 'graph') goView('code'); setTestOpen((t) => !t) }}>Test</Button>
            <Button variant="ghost" size="xs" aria-pressed={settingsOpen} ref={settingsButton}
              onClick={() => { if (!settingsOpen) captureScroll(); setSettingsOpen((s) => !s) }}>Settings</Button>
          </div>
          <div className="nb-toolbar-side">
            <NotebookViewToggle view={view} onChange={(v) => { setSettingsOpen(false); goView(v) }} />
            <Button variant="primary" size="xs" onClick={onPublish} disabled={readOnly || !canRun}>
              <SwapLabel show={busy === 'publish' ? 'Publishing…' : 'Publish…'} options={['Publish…', 'Publishing…']} />
            </Button>
          </div>
        </div>
      </div>

      <aside className="appnb-rail" aria-label="Notebook panels">
        <Button variant="ghost" size="xs" className="nb-icon-btn" ref={sectionsButton}
          aria-label="Sections" title="Sections" aria-pressed={rail === 'sections'}
          onClick={() => setRail((r) => (r === 'sections' ? null : 'sections'))}>
          <svg aria-hidden="true" viewBox="0 0 24 24" fill="none" stroke="currentColor"
            strokeWidth="1.6" strokeLinecap="round">
            <path d="M9 4h11M9 12h11M9 20h11" />
            <path d="M4 4h.01M4 12h.01M4 20h.01" strokeWidth="3" />
          </svg>
        </Button>
        <Button variant="ghost" size="xs" className="nb-icon-btn"
          aria-label="Extensions" title="Extensions" aria-pressed={rail === 'extensions'}
          onClick={() => setRail((r) => (r === 'extensions' ? null : 'extensions'))}>
          <svg aria-hidden="true" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="1.6">
            <rect x="4" y="4" width="7" height="7" rx="1" />
            <rect x="13" y="4" width="7" height="7" rx="1" />
            <rect x="4" y="13" width="7" height="7" rx="1" />
            <rect x="13" y="13" width="7" height="7" rx="1" />
          </svg>
        </Button>
        <Button variant="ghost" size="xs" className="nb-icon-btn"
          aria-label="Modules" title="Modules" aria-pressed={rail === 'modules'}
          onClick={() => setRail((r) => (r === 'modules' ? null : 'modules'))}>
          <svg aria-hidden="true" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="1.6" strokeLinejoin="round">
            <path d="M12 3l8 4.5v9L12 21l-8-4.5v-9L12 3z" />
            <path d="M12 12l8-4.5M12 12v9M12 12L4 7.5" />
          </svg>
        </Button>
        <Button variant="ghost" size="xs" className="nb-icon-btn"
          aria-label="Policies" title="Policies" aria-pressed={rail === 'policies'}
          onClick={() => setRail((r) => (r === 'policies' ? null : 'policies'))}>
          <svg aria-hidden="true" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="1.6" strokeLinejoin="round">
            <path d="M12 3l7 3v5c0 4.5-3 8.5-7 10-4-1.5-7-5.5-7-10V6l7-3z" />
            <path d="M9 12l2 2 4-4" strokeLinecap="round" />
          </svg>
        </Button>
      </aside>

      <aside className="appnb-explorer" hidden={rail === null}>
        <div className="appnb-pane" hidden={rail !== 'sections'}>
          <div className="nb-outline-head">
            <h2 className="nb-outline-title">Sections</h2>
            <label className="nb-outline-filter">
              <span className="sr-only">Filter sections</span>
              <input type="search" value={dirFilter} placeholder="Filter"
                aria-label="Filter sections" onChange={(e) => setDirFilter(e.target.value)} />
            </label>
          </div>
          <nav className="nb-directory" aria-label="Section directory">
            {ordered.map((s, i) => dirMatch(s.id) ? (
              <button key={s.id} type="button" className="nb-dir-row"
                title={view === 'graph' && !structure.names.includes(s.id)
                  ? 'Save this section to show it in Graph' : s.id}
                aria-current={activeNav === s.id ? 'true' : undefined}
                disabled={view === 'graph' && !structure.names.includes(s.id)}
                onClick={() => pick(s.id)}>
                <span className="nb-dir-ord">{i + 1}.</span>
                <span className="nb-dir-name">{s.id}</span>
                {labelFor(s.id) && <span className="appnb-dir-entry">{labelFor(s.id)}</span>}
              </button>
            ) : null)}
          </nav>
        </div>
        <div className="appnb-pane" hidden={rail !== 'extensions'}>
          <div className="nb-outline-head">
            <h2 className="nb-outline-title">Extensions</h2>
          </div>
          <div className="appnb-catalog" ref={catalogRefs.extensions} />
        </div>
        <div className="appnb-pane" hidden={rail !== 'modules'}>
          <div className="nb-outline-head">
            <h2 className="nb-outline-title">Modules</h2>
          </div>
          <div className="appnb-catalog" ref={catalogRefs.modules} />
        </div>
        <div className="appnb-pane" hidden={rail !== 'policies'}>
          <div className="nb-outline-head">
            <h2 className="nb-outline-title">Policies</h2>
          </div>
          <div className="appnb-catalog" ref={catalogRefs.policies} />
        </div>
      </aside>

      <div className="nb-code appnb-code" hidden={view === 'graph' || settingsOpen} ref={codeRef}>
        <div className="nb-cards">
          {ordered.map((sec, i) => (
            <Fragment key={sec.id}>
              <AppSectionCard sec={sec} index={i} count={ordered.length - (mainId ? 1 : 0)}
                ids={orderedIds}
                movable={!readOnly && sec.id !== mainId}
                onOpenTest={sec.id === mainId ? openTest : undefined}
                canDelete={ordered.length > 1}
                canAdd={sections.length < MAX_SECTIONS}
                extensions={extensions} readOnly={readOnly}
                entryLabel={labelFor(sec.id)}
                dropEdge={dropEdgeOf(sec.id)}
                focusOnMount={() => {
                  if (focusNext.current !== sec.id) return false
                  focusNext.current = ''
                  return true
                }}
                handlers={handlers} />
              {sec.id !== mainId && (
                <NotebookInsert name={sec.id} disabled={sections.length >= MAX_SECTIONS}
                  onInsert={readOnly ? undefined : () => addSection(sec.id)} />
              )}
            </Fragment>
          ))}
        </div>
      </div>

      {/* The Settings workspace replaces the canvas — cells stay mounted
          behind hidden so drafts and undo survive (C2). */}
      <section className="appnb-settings" aria-label="Notebook settings" hidden={!settingsOpen}>
        {settings(catalogTargets, openSettings, closeSettings)}
      </section>

      <NotebookGraphPane hidden={view !== 'graph' || settingsOpen}>
        <NotebookGraph structure={structure} metas={EMPTY_METAS}
          selected={graphSelected}
          onSelect={(n) => navigate('graph', n)}
          onClear={() => navigate('graph', '')}
          onOpenCode={openGraphCode}
          entryLabels={entryLabels} />
      </NotebookGraphPane>

      <div className="appnb-utility" hidden={!testVisible}>
        <section className="appnb-utility-panel" aria-label="Test notebook" hidden={!testVisible}>
          <div className="appnb-utility-head">
            <h2 className="nb-outline-title" tabIndex={-1} ref={testHead}>Test</h2>
            <Button variant="primary" size="sm" onClick={onRun} disabled={!canRun}
              aria-busy={busy === 'run'} data-testid="app-run">
              <SwapLabel show={busy === 'run' ? 'Testing…' : 'Test main'} options={['Test main', 'Testing…']} />
            </Button>
          </div>
          {test}
        </section>
      </div>

      <div className="nb-statusbar appnb-statusbar">
        <span className="nb-status" role="status">
          <span className="nb-status-text">{saveStatus}</span>
          {view === 'graph' && <span className="appnb-note" title={graphStatus}>{graphStatus}</span>}
        </span>
      </div>

      {removeId && (
        <Confirm tone="danger" title="Remove section" confirm="Remove" pending="Removing…"
          onClose={() => setRemoveId(null)}
          action={async () => { doRemove() }}>
          Remove section “{removeId}”? Sections reading its defines will fail to run.
        </Confirm>
      )}
    </div>
  )
}
