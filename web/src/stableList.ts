import { useRef, useState } from 'react'

// Where a row is shown: its identity and the group (slot) it sits in.
export type Placement = { key: string; slot: string }

export type Held = { placements: Placement[]; reset: unknown }

// hold decides which order the user sees (S5 in
// docs/interaction-stability.md). The first data and a change of view the
// user made (reset) are shown as they come; rows the user caused (mine)
// are shown at once at the top of a newest-first list; everything else
// keeps the order the user last saw. Returns prev when nothing changes.
export function hold(prev: Held | null, fresh: readonly Placement[], reset: unknown, mine: (key: string) => boolean): Held {
  if (!prev || prev.reset !== reset) return { placements: [...fresh], reset }
  const known = new Set(prev.placements.map((p) => p.key))
  const adopted = fresh.filter((p) => !known.has(p.key) && mine(p.key))
  return adopted.length ? { placements: [...adopted, ...prev.placements], reset } : prev
}

// A placement a caller asked to keep even though it left the fresh
// list — rendered as an inert placeholder until the reveal (S3/F1).
export type Shown = Placement & { gone?: boolean }

// reconcile shows held rows with fresh membership: rows already shown keep
// their order and group, rows that are gone drop out — unless keepGone is
// set, in which case they stay in place marked {gone:true} — and arrivals
// or moves are only counted so the list can offer to show them. `gone`
// counts the placeholders so the caller can offer the reveal even with
// zero arrivals.
export function reconcile(held: readonly Placement[], fresh: readonly Placement[], opts: { keepGone?: boolean } = {}) {
  const now = new Set(fresh.map((p) => p.key))
  const shown: Shown[] = []
  for (const p of held) {
    if (now.has(p.key)) shown.push(p)
    else if (opts.keepGone) shown.push({ ...p, gone: true })
  }
  const gone = shown.length - shown.filter((p) => !p.gone).length
  const known = new Set(held.map((p) => p.key))
  const arrived = fresh.filter((p) => !known.has(p.key)).length
  const order = (ps: readonly Placement[]) => ps.map((p) => `${p.slot}\u0000${p.key}`).join('\u0001')
  const present = shown.filter((p) => !p.gone)
  const moved = order(present) !== order(fresh.filter((p) => known.has(p.key)))
  return { shown, arrived, moved, gone }
}

// useHeldOrder renders a polled list without moving rows under the user:
// fresh data updates rows in place, while arrivals and reordering wait
// for reveal(), which the list offers as "Show N new" / "Show latest".
export function useHeldOrder<T>(items: readonly T[] | undefined, place: (item: T) => Placement, opts: { reset?: unknown; mine?: (item: T) => boolean; keepGone?: boolean } = {}) {
  const [state, setState] = useState<Held | null>(null)
  // lastSeen keeps the most recent object for every shown key so a
  // keepGone placeholder can render the row it replaces (S3/F1).
  const lastSeen = useRef(new Map<string, T>())
  const placed = (items ?? []).map((item) => ({ item, at: place(item) }))
  const fresh = placed.map((p) => p.at)
  const byKey = new Map(placed.map((p) => [p.at.key, p.item]))
  for (const p of placed) lastSeen.current.set(p.at.key, p.item)
  let held = state
  if (items) {
    const next = hold(state, fresh, opts.reset, (key) => !!opts.mine?.(byKey.get(key)!))
    if (next !== state) { held = next; setState(next) }
  }
  const r = reconcile(held?.placements ?? [], fresh, { keepGone: opts.keepGone })
  return {
    rows: r.shown.map((p) => ({ item: (byKey.get(p.key) ?? lastSeen.current.get(p.key))!, slot: p.slot, gone: p.gone === true })),
    arrived: r.arrived,
    moved: r.moved,
    gone: r.gone,
    reveal: () => setState({ placements: fresh, reset: opts.reset }),
  }
}
