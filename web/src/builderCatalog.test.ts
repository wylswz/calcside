import test from 'node:test'
import assert from 'node:assert/strict'
import { filterExtensions, paginateExtensions } from './builderCatalog.ts'

const mk = (n: number) => Array.from({ length: n }, (_, i) => ({
  source: `contrib/ext${i}@0.1.0`, family: `contrib/ext${i}`, version: '0.1.0', sum: `h1:sum${i}`, name: `ext-${String(i).padStart(4, '0')}`, description: `Extension ${i} description`,
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

// EP-0020 console part 1: family grouping and version ordering.
import { addedVersion, compareVersions, groupExtensions, filterFamilies, findExtension } from './builderCatalog.ts'

test('compareVersions natural ordering', () => {
  assert.ok(compareVersions('0.10.0', '0.9.0') > 0)
  assert.ok(compareVersions('1.0.0', '1.0.0-rc1') > 0)
  assert.ok(compareVersions('v1.2', '1.1') > 0)
  assert.ok(compareVersions('0.1.1', '0.1.0') > 0)
  const shaA = 'a'.repeat(40), shaB = 'b'.repeat(40)
  assert.equal(Math.sign(compareVersions(shaA, shaB)), -Math.sign(compareVersions(shaB, shaA)))
  assert.equal(compareVersions('1.0.0', '1.0.0'), 0)
})

const fam = (v: string, over: Partial<Parameters<typeof groupExtensions>[0][number]> = {}) => ({
  source: `contrib/tool@${v}`, family: 'contrib/tool', version: v, sum: `h1:${v}`,
  name: 'tool', dependencies: [], ops: [], config: [], ...over,
})

test('groupExtensions orders versions newest first and keeps first-appearance order', () => {
  const other = { ...fam('9.9'), family: 'contrib/other', source: 'contrib/other@9.9', name: 'other' }
  const g = groupExtensions([fam('0.9.0'), other, fam('0.10.0'), fam('0.10.0-rc1')])
  assert.deepEqual(g.map((f) => f.family), ['contrib/tool', 'contrib/other'])
  assert.deepEqual(g[0].versions.map((v) => v.version), ['0.10.0', '0.10.0-rc1', '0.9.0'])
  assert.equal(g[0].latest.version, '0.10.0')
})

test('filterFamilies matches any version plus the family string', () => {
  const fs = groupExtensions([fam('0.1.0'), fam('0.2.0', { description: 'needs net' }), { ...fam('1.0'), family: 'contrib/other', source: 'contrib/other@1.0', name: 'other' }])
  assert.equal(filterFamilies(fs, 'needs net').length, 1)
  assert.equal(filterFamilies(fs, 'contrib/tool').length, 1)
  assert.equal(filterFamilies(fs, '0.2.0').length, 1)
  assert.equal(filterFamilies(fs, '').length, 2)
})

test('findExtension: exact, legacy sole-candidate, legacy ambiguous (EP-0020)', () => {
  const cat = [fam('0.1.0'), fam('0.2.0'), { ...fam('1.0'), family: 'contrib/sole', source: 'contrib/sole@1.0' }]
  assert.equal(findExtension(cat, 'contrib/tool@0.2.0')?.version, '0.2.0')
  assert.equal(findExtension(cat, 'contrib/sole')?.source, 'contrib/sole@1.0')
  assert.equal(findExtension(cat, 'contrib/tool'), undefined)
  assert.equal(findExtension(cat, 'contrib/tool@9.9'), undefined)
})

test('addedVersion: one added version per family, legacy bare-family sole version (EP-0020)', () => {
  const fs = groupExtensions([fam('0.1.0'), fam('0.2.0'), { ...fam('1.0'), family: 'contrib/solo', source: 'contrib/solo@1.0' }])
  const tool = fs.find((f) => f.family === 'contrib/tool')!
  const solo = fs.find((f) => f.family === 'contrib/solo')!
  assert.equal(addedVersion(tool, new Set(['contrib/tool@0.1.0']))?.version, '0.1.0')
  assert.equal(addedVersion(tool, new Set(['contrib/tool@0.1.0', 'contrib/tool@0.2.0']))?.version, '0.2.0')
  assert.equal(addedVersion(tool, new Set(['contrib/tool'])), undefined)
  assert.equal(addedVersion(solo, new Set(['contrib/solo']))?.version, '1.0')
  assert.equal(addedVersion(tool, new Set()), undefined)
})
