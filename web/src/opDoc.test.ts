import { test } from 'node:test'
import assert from 'node:assert/strict'
import { formatOpDoc } from './opDoc.ts'
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
