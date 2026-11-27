import { test } from 'node:test'
import assert from 'node:assert/strict'
import { entryFunctions } from './entryFunctions.ts'

test('main and helpers are entries', () => {
  assert.deepEqual(entryFunctions('def main(input):\n    return input\n'), [{ name: 'main', line: 1 }])
  assert.deepEqual(entryFunctions('x = 1\n\ndef helper(ctx):\n    return ctx\n'), [{ name: 'helper', line: 3 }])
})

test('a default parameter still counts as one argument', () => {
  assert.deepEqual(entryFunctions('def on_ok(event={}):\n    return event\n'), [{ name: 'on_ok', line: 1 }])
  assert.deepEqual(entryFunctions('def f(x=foo(1, 2)):\n    return x\n'), [{ name: 'f', line: 1 }])
  assert.deepEqual(entryFunctions('def f(x=y):\n    return x\n'), [{ name: 'f', line: 1 }])
})

test('two parameters are not an entry', () => {
  assert.deepEqual(entryFunctions('def f(a, b):\n    return a\n'), [])
  assert.deepEqual(entryFunctions('def f(a=1, b=2):\n    return a\n'), [])
})

test('positional-only and typed params are not entries', () => {
  assert.deepEqual(entryFunctions('def f(x, /):\n    return x\n'), [])
  assert.deepEqual(entryFunctions('def f(x: int):\n    return x\n'), [])
})

test('multiline signatures still count, reporting the def line', () => {
  assert.deepEqual(entryFunctions('def f(\n  x=foo(1, 2),\n):\n    return x\n'), [{ name: 'f', line: 1 }])
  assert.deepEqual(entryFunctions('a = 1\n\ndef g(\n    q,\n):\n    return q\n'), [{ name: 'g', line: 3 }])
})

test('defs inside strings or nested scopes are not entries', () => {
  assert.deepEqual(entryFunctions('s = """\ndef fake(x):\n    pass\n"""\n'), [])
  assert.deepEqual(entryFunctions("s = 'def nope(x):\\n    pass'\n"), [])
})

test('*args and **kwargs are not entries', () => {
  assert.deepEqual(entryFunctions('def f(*args):\n    pass\ndef g(**kw):\n    pass\n'), [])
})

test('indented defs are not top-level', () => {
  assert.deepEqual(entryFunctions('def main(i):\n    def inner(x):\n        return x\n    return inner(i)\n'), [{ name: 'main', line: 1 }])
})

test('_private names are not entries', () => {
  assert.deepEqual(entryFunctions('def _private(x):\n    return x\n'), [])
})

test('whitespace and trailing comments are tolerated', () => {
  assert.deepEqual(entryFunctions('def main( input ) :   # entry point\n    return input\n'), [{ name: 'main', line: 1 }])
})

test('multiple defs per section keep source order and lines', () => {
  const code = 'def a(x):\n    return x\n\ny = 2\ndef b(q):\n    return q\ndef main(input):\n    return input\n'
  assert.deepEqual(entryFunctions(code), [
    { name: 'a', line: 1 },
    { name: 'b', line: 5 },
    { name: 'main', line: 7 },
  ])
})
