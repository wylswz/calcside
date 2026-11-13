import { expressionInterpreter } from 'vega-interpreter'
import type { EmbedOptions } from 'vega-embed'
import type { Loader } from 'vega'

// chartEmbed builds the vega-embed options for a stored chart display
// (EP-0024). It imports vega-interpreter at module level, so it is
// only loaded lazily inside ChartView's effect together with
// vega-embed — never eagerly — keeping the vega stack out of the
// console's initial chunk.
//
// Every loader method is denied: stored specs are self-contained
// (data.values inline, no urls) so no network, file, or resource
// loading may ever happen — a hostile or malformed spec cannot turn
// the console into a fetch oracle.
export function denyAllLoader(): Loader {
  const deny = <T>(method: string) => (): Promise<T> =>
    Promise.reject(new Error(`chart loader ${method} disabled`))
  return {
    load: deny('load'),
    sanitize: deny('sanitize'),
    http: deny('http'),
    file: deny('file'),
  }
}

// chartEmbedOptions is the single embed configuration for chart
// displays: AST mode with the expression interpreter, SVG output, the
// deny-all loader, and only export/source actions (no editor, no
// compiled view). dark selects vega-themes' dark palette so marks and
// labels stay legible on the dark console; the background is always
// transparent so the card's own surface shows through.
export function chartEmbedOptions(dark: boolean): EmbedOptions {
  return {
    ast: true,
    expr: expressionInterpreter,
    renderer: 'svg',
    loader: denyAllLoader(),
    ...(dark ? { theme: 'dark' as const } : {}),
    config: { background: 'transparent' },
    actions: { export: true, source: true, compiled: false, editor: false },
  }
}
