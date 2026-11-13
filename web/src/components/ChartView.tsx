import { useEffect, useRef, useState } from 'react'
import type { Display } from '../api'
import type { TopLevelSpec } from 'vega-lite'
import { chartReservedBox } from '../chartBox'

// ChartView embeds one Vega-Lite display spec (EP-0024). Vega-Lite and
// vega-embed are imported dynamically inside the effect so they land in
// their own chunk; the parent already reserved the box via
// chartReservedBox, so the chunk arriving causes no layout shift.
//
// The effect is keyed on the serialized spec: a poll that returns the
// same execution re-renders with an identical specJson and does NOT
// re-embed, preserving zoom/brush interaction state (C invariant).
export default function ChartView({ display }: { display: Display }) {
  const host = useRef<HTMLDivElement>(null)
  const [failed, setFailed] = useState(false)
  const specJson = JSON.stringify(display.spec)

  useEffect(() => {
    const el = host.current
    if (!el) return
    let live = true
    let finalize: (() => void) | undefined
    el.replaceChildren()
    setFailed(false)
    void Promise.all([import('vega-embed'), import('../chartEmbed')])
      .then(([{ default: embed }, { chartEmbedOptions }]) => {
        if (!live) return undefined
        // Color scheme is read once at embed time; a later OS theme
        // change does not re-embed (interaction state is preserved).
        const dark = window.matchMedia('(prefers-color-scheme: dark)').matches
        return embed(el, JSON.parse(specJson) as TopLevelSpec, chartEmbedOptions(dark))
      })
      .then((r) => {
        if (!r) return
        if (!live) {
          r.finalize()
          return
        }
        finalize = r.finalize
      })
      .catch(() => {
        if (live) setFailed(true)
      })
    return () => {
      live = false
      finalize?.()
      el.replaceChildren()
    }
  }, [specJson])

  const box = chartReservedBox(display.spec)
  return (
    <div className="nb-chart" style={{ minHeight: box.minHeight, maxWidth: box.maxWidth }}>
      <div ref={host} className="nb-chart-host" />
      {failed && <p className="nb-chart-note">chart unavailable</p>}
    </div>
  )
}
