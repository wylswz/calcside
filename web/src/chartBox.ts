// chartReservedBox reserves layout space from a display spec's declared
// size before the lazy vega chunk loads (S invariant): min-height is
// the spec height (default 280) + 64 for the title/actions row; width
// is the spec width + 120 capped at 100%. min-height (not height)
// means the box can grow for legends/tooltips but never shrinks below
// the reservation once rendered. Kept free of vega imports so both the
// Suspense fallback and ChartView share it without pulling the chart
// stack into the main bundle.
export function chartReservedBox(spec: Record<string, unknown>): { minHeight: number; maxWidth: string } {
  const dim = (v: unknown, dflt: number) =>
    typeof v === 'number' && Number.isFinite(v) && v > 0 ? v : dflt
  const h = dim(spec.height, 280)
  const w = dim(spec.width, 480)
  return { minHeight: h + 64, maxWidth: `min(${w + 120}px, 100%)` }
}
