// Defence-in-depth spec screening for the renderer. The API tier
// already validates specs against the Vega-Lite subset (internal/chart);
// this pass re-checks the two properties the renderer cannot allow at
// all: any url/href key anywhere in the tree (data sources, href
// channels, mark properties) and the image/geoshape marks that would
// pull external resources.

export class SpecError extends Error {
  constructor(msg) {
    super(msg);
    this.name = 'SpecError';
    this.code = 'spec';
  }
}

const DENIED_KEYS = new Set(['url', 'href']);
const DENIED_MARKS = new Set(['image', 'geoshape']);

// markDenied reports whether a mark value names a denied mark.
// Vega-Lite marks are strings ("line"), objects ({type: "line"}), or
// arrays of either.
function markDenied(mark) {
  if (typeof mark === 'string') return DENIED_MARKS.has(mark);
  if (Array.isArray(mark)) return mark.some(markDenied);
  if (mark !== null && typeof mark === 'object') return DENIED_MARKS.has(mark.type);
  return false;
}

// checkSpec walks the decoded spec iteratively — no recursion limit
// surprises on deep trees — and throws SpecError on the first denied
// node. Errors never include spec contents.
export function checkSpec(spec) {
  const stack = [spec];
  while (stack.length > 0) {
    const v = stack.pop();
    if (Array.isArray(v)) {
      for (const e of v) stack.push(e);
      continue;
    }
    if (v === null || typeof v !== 'object') continue;
    for (const [k, val] of Object.entries(v)) {
      if (DENIED_KEYS.has(k)) throw new SpecError('spec contains a denied key');
      if (k === 'mark' && markDenied(val)) throw new SpecError('spec uses a denied mark');
      stack.push(val);
    }
  }
}
