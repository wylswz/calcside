import { test } from 'node:test'
import assert from 'node:assert/strict'
import { formatOpDoc, formatParamDetail, formatParamDoc } from './opDoc.ts'
import type { components } from './api/schema'

type OpDoc = components['schemas']['OpDoc']

const line: OpDoc = {
  name: 'line',
  doc: 'Line chart spec.',
  params: ['data', 'x', 'y', 'options'],
  param_docs: [
    { name: 'data', type: 'list[dict]', doc: 'Rows of flat dicts.' },
    { name: 'x', type: 'string', doc: 'Field name.' },
    { name: 'y', type: 'string', doc: 'Field name.' },
    {
      name: 'options', type: 'dict', doc: 'Chart options.', optional: true,
      fields: [
        { name: 'color', type: 'string', doc: 'Colour field.', default: null },
        { name: 'tooltip', type: 'bool', default: true },
        { name: 'title', type: 'string', default: 'My chart' },
      ],
    },
  ],
  returns: 'dict — spec',
  example: 'spec = ext.charts.line(data, "x", "y")\nio.display(spec)',
}

test('formatOpDoc renders the OpDetail layout', () => {
  const text = formatOpDoc('ext.charts.line', line)
  const expected = [
    '- `ext.charts.line(data, x, y, options)` — Line chart spec.',
    '  Parameters:',
    '  - `data` (list[dict]): Rows of flat dicts.',
    '  - `x` (string): Field name.',
    '  - `y` (string): Field name.',
    '  - `options` (dict, optional): Chart options.',
    '    - `color` (string, default None): Colour field.',
    '    - `tooltip` (bool, default True)',
    '    - `title` (string, default "My chart")',
    '  Returns: dict — spec',
    '  Example:',
    '    spec = ext.charts.line(data, "x", "y")',
    '    io.display(spec)',
  ].join('\n')
  assert.equal(text, expected)
})

test('formatOpDoc omits empty parts', () => {
  assert.equal(formatOpDoc('fs.read', { name: 'read', doc: 'Read a file.', params: ['path'] }),
    '- `fs.read(path)` — Read a file.')
  assert.equal(formatOpDoc('fs.list', { name: 'list', params: ['dir'] }),
    '- `fs.list(dir)`')
  // Returns without param docs still renders.
  assert.equal(formatOpDoc('x.y', { name: 'y', returns: 'int' }),
    '- `x.y()`\n  Returns: int')
})

test('formatParamDetail renders type, requirement and declared defaults', () => {
  assert.equal(formatParamDetail({ name: 'data', type: 'list[dict]' }), 'list[dict], required')
  assert.equal(formatParamDetail({ name: 'x', type: 'string', optional: true }), 'string, optional')
  assert.equal(formatParamDetail({ name: 'options', type: 'dict', optional: true, default: null }), 'dict, optional, default None')
  assert.equal(formatParamDetail({ name: 'count', type: 'int', default: 0 }), 'int, required, default 0')
  assert.equal(formatParamDetail({ name: 'flag', type: 'bool', default: false }), 'bool, required, default False')
  assert.equal(formatParamDetail({ name: 'label', type: 'string', default: '' }), 'string, required, default ""')
  assert.equal(formatParamDetail({ name: 'bare' }), 'required')
})

test('formatParamDoc renders the doc and dict fields for completion info', () => {
  assert.equal(formatParamDoc({ name: 'data', doc: 'Input rows.' }), 'Input rows.')
  assert.equal(formatParamDoc({ name: 'options', doc: 'Chart options.', fields: [{ name: 'title', type: 'string', doc: 'Chart title' }] }),
    'Chart options.\n\nFields:\n- `title` (string): Chart title')
  assert.equal(formatParamDoc({ name: 'o', fields: [{ name: 'bins', type: 'int', default: 30 }, { name: 'color', type: 'string', default: null }] }),
    'Fields:\n- `bins` (int, default 30)\n- `color` (string, default None)')
  assert.equal(formatParamDoc({ name: 'bare' }), '')
})
