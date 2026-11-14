import * as vega from 'vega';
import { compile } from 'vega-lite';
import { expressionInterpreter } from 'vega-interpreter';
import { Resvg } from '@resvg/resvg-js';

import { fontFiles } from './fonts.js';
import { checkSpec, SpecError } from './spec-check.js';

export const PIXEL_CAP = 4096;
const MIN_SCALE = 0.25;
const MAX_SCALE = 4;

export class RenderError extends Error {
  constructor(msg) {
    super(msg);
    this.name = 'RenderError';
    this.code = 'render';
  }
}

// denyAllLoader blocks every external load path inside Vega: data
// urls, href channels, image marks and file/http access all reject.
// Stored specs are self-contained (inline data.values, no urls) so
// nothing legitimate needs loading — defence in depth on top of the
// spec screening.
export function denyAllLoader() {
  const deny = (method) => () => Promise.reject(new Error(`loader ${method} disabled`));
  return { load: deny('load'), sanitize: deny('sanitize'), http: deny('http'), file: deny('file') };
}

// svgSize pulls the root <svg> width/height attributes. Vega emits
// them as plain numbers in CSS px.
export function svgSize(svg) {
  const tag = svg.slice(0, svg.indexOf('>'));
  const wM = /\swidth="([\d.]+)"/.exec(tag);
  const hM = /\sheight="([\d.]+)"/.exec(tag);
  if (!wM || !hM) throw new RenderError('svg has no width/height');
  const w = Number(wM[1]);
  const h = Number(hM[1]);
  if (!Number.isFinite(w) || !Number.isFinite(h) || w <= 0 || h <= 0) {
    throw new RenderError('bad svg size');
  }
  return { w, h };
}

// renderPng turns one validated Vega-Lite spec into PNG bytes.
// opts: {scale, maxSide}. Throws SpecError for denied/malformed input
// (→ 400) and RenderError for compile/render/pixel-cap failures (→ 422).
export async function renderPng(spec, opts = {}) {
  checkSpec(spec);
  if (spec === null || typeof spec !== 'object' || Array.isArray(spec)) {
    throw new SpecError('spec must be a JSON object');
  }
  const scale = opts.scale == null ? 1 : Number(opts.scale);
  if (!Number.isFinite(scale)) throw new SpecError('scale must be a number');
  const zoom0 = Math.min(MAX_SCALE, Math.max(MIN_SCALE, scale));
  let maxSide = null;
  if (opts.maxSide != null) {
    maxSide = Number(opts.maxSide);
    if (!Number.isInteger(maxSide) || maxSide <= 0) {
      throw new SpecError('max_side must be a positive integer');
    }
  }

  let vgSpec;
  try {
    vgSpec = compile(spec).spec;
  } catch {
    throw new RenderError('vega-lite compile failed');
  }
  let runtimeSpec;
  try {
    runtimeSpec = vega.parse(vgSpec, null, { ast: true });
  } catch {
    throw new RenderError('vega parse failed');
  }
  let svg;
  try {
    const view = new vega.View(runtimeSpec, {
      expr: expressionInterpreter,
      renderer: 'none',
      loader: denyAllLoader(),
    });
    svg = await view.toSVG();
  } catch {
    throw new RenderError('render failed');
  }
  const { w, h } = svgSize(svg);

  // max_side shrinks the scaled result so the longest side lands on
  // max_side; the hard pixel cap is checked after that shrink.
  let zoom = zoom0;
  const longest = Math.max(w, h);
  if (maxSide != null && longest * zoom > maxSide) zoom = maxSide / longest;
  if (Math.ceil(w * zoom) > PIXEL_CAP || Math.ceil(h * zoom) > PIXEL_CAP) {
    throw new RenderError('pixel cap exceeded');
  }
  const target = w >= h
    ? { mode: 'width', value: Math.max(1, Math.round(w * zoom)) }
    : { mode: 'height', value: Math.max(1, Math.round(h * zoom)) };

  try {
    const resvg = new Resvg(svg, {
      fitTo: target,
      font: {
        fontFiles: fontFiles(),
        loadSystemFonts: false,
        defaultFontFamily: 'IBM Plex Sans',
        // Vega-Lite emits the generic "sans-serif" family; map it onto
        // the embedded IBM Plex Sans faces.
        sansSerifFamily: 'IBM Plex Sans',
      },
      // Agents need an opaque image; a spec-provided background wins
      // because it draws inside the SVG over this underlay.
      background: 'white',
    });
    const rendered = resvg.render();
    return { png: rendered.asPng(), width: rendered.width, height: rendered.height };
  } catch {
    throw new RenderError('rasterize failed');
  }
}
