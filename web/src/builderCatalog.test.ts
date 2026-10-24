import test from 'node:test'
import assert from 'node:assert/strict'
import { filterExtensions, paginateExtensions } from './builderCatalog.ts'

const mk = (n: number) => Array.from({ length: n }, (_, i) => ({
  source: `contrib/ext${i}`, name: `ext-${String(i).padStart(4, '0')}`, description: `Extension ${i} description`,
  dependencies: [], ops: [], config: [], icon_url: undefined,
}))

test('filterExtensions matches name/source/description case-insensitively', () => {
  const entries = mk(1000)
  assert.equal(filterExtensions(entries, '').length, 1000)
  assert.equal(filterExtensions(entries, '  ').length, 1000)
  const hit = filterExtensions(entries, 'EXT-0999')
  assert.equal(hit.length, 1)
  assert.equal(hit[0].name, 'ext-0999')
  assert.equal(filterExtensions(entries, 'extension 999 description').length > 0, true)
  assert.equal(filterExtensions(entries, 'no-such-thing').length, 0)
})

test('paginateExtensions stays bounded at scale and clamps', () => {
  const entries = mk(1000)
  const p0 = paginateExtensions(entries, 0)
  assert.equal(p0.items.length, 12)
  assert.equal(p0.total, 1000)
  assert.equal(p0.pages, 84)
  assert.equal(p0.start, 1)
  assert.equal(p0.end, 12)
  const last = paginateExtensions(entries, 83)
  assert.equal(last.items.length, 4) // 1000 % 12
  // out-of-range pages clamp instead of going blank
  assert.equal(paginateExtensions(entries, 999).page, 83)
  assert.equal(paginateExtensions(entries, -5).page, 0)
})
