import { useState } from 'react'

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

// reconcile shows held rows with fresh membership: rows already shown keep
// their order and group, rows that are gone drop out, and arrivals or
// moves are only counted so the list can offer to show them.
export function reconcile(held: readonly Placement[], fresh: readonly Placement[]) {
  const now = new Set(fresh.map((p) => p.key))
  const shown = held.filter((p) => now.has(p.key))
  const known = new Set(held.map((p) => p.key))
  const arrived = fresh.filter((p) => !known.has(p.key)).length
  const order = (ps: readonly Placement[]) => ps.map((p) => `${p.slot}\u0000${p.key}`).join('\u0001')
  const moved = order(shown) !== order(fresh.filter((p) => known.has(p.key)))
  return { shown, arrived, moved }
}

// useHeldOrder renders a polled list without moving rows under the user:
// fresh data updates rows in place, while arrivals and reordering wait
// for reveal(), which the list offers as "Show N new" / "Show latest".
export function useHeldOrder<T>(items: readonly T[] | undefined, place: (item: T) => Placement, opts: { reset?: unknown; mine?: (item: T) => boolean } = {}) {
  const [state, setState] = useState<Held | null>(null)
  const placed = (items ?? []).map((item) => ({ item, at: place(item) }))
  const fresh = placed.map((p) => p.at)
  const byKey = new Map(placed.map((p) => [p.at.key, p.item]))
  let held = state
  if (items) {
    const next = hold(state, fresh, opts.reset, (key) => !!opts.mine?.(byKey.get(key)!))
    if (next !== state) { held = next; setState(next) }
  }
  const r = reconcile(held?.placements ?? [], fresh)
  return {
    rows: r.shown.map((p) => ({ item: byKey.get(p.key)!, slot: p.slot })),
    arrived: r.arrived,
    moved: r.moved,
    reveal: () => setState({ placements: fresh, reset: opts.reset }),
  }
}
