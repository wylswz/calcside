import assert from 'node:assert/strict'
import { test } from 'node:test'

test('console authentication client', async (t) => {
  const redirects: string[] = []
  const requests: Request[] = []
  let pathname = '/login'
  let status = 200
  const originalWindow = Object.getOwnPropertyDescriptor(globalThis, 'window')
  Object.defineProperty(globalThis, 'window', {
    configurable: true,
    value: {
      location: { get pathname() { return pathname }, assign: (path: string) => redirects.push(path) },
    },
  })
  t.after(() => {
    if (originalWindow) Object.defineProperty(globalThis, 'window', originalWindow)
    else Reflect.deleteProperty(globalThis, 'window')
  })
  t.beforeEach(() => {
    redirects.length = 0
    requests.length = 0
    pathname = '/login'
    status = 200
  })
  t.mock.method(globalThis, 'fetch', async (input: Request) => {
    requests.push(input)
    const body = status === 200 ? { ok: true } : { error: { code: 'auth_failed', message: 'invalid username or password' } }
    const response = Response.json(body, { status })
    Object.defineProperty(response, 'url', { value: input.url })
    return response
  })
  const NativeRequest = globalThis.Request
  t.mock.method(globalThis, 'Request', class extends NativeRequest {
    constructor(input: RequestInfo | URL, init?: RequestInit) {
      super(typeof input === 'string' ? new URL(input, 'http://localhost') : input, init)
    }
  })
  const { api, ApiError } = await import('./api.ts')

  await t.test('Basic login sends UTF-8 credentials once in the Authorization header', async () => {
    assert.deepEqual(await api.basicLogin('admin', '密码:with:colons'), { ok: true })
    assert.equal(requests.length, 1)
    const request = requests[0]
    assert.equal(new URL(request.url).pathname, '/auth/basic/login')
    assert.equal(request.method, 'POST')
    assert.equal(request.headers.get('X-Requested-With'), 'calcside')
    assert.equal(request.credentials, 'same-origin')
    const authorization = request.headers.get('Authorization')!
    assert.ok(authorization.startsWith('Basic '))
    assert.equal(Buffer.from(authorization.slice(6), 'base64').toString('utf8'), 'admin:密码:with:colons')
    assert.equal(await request.text(), '')
    await api.get('/api/v1/me')
    assert.equal(requests[1].headers.get('Authorization'), null)
    assert.deepEqual(redirects, [])
  })

  await t.test('invalid Basic credentials surface an inline error without redirecting', async () => {
    pathname = '/'
    status = 401
    await assert.rejects(api.basicLogin('admin', 'wrong'), (error: unknown) => {
      assert.ok(error instanceof ApiError)
      assert.equal(error.status, 401)
      assert.equal(error.message, 'invalid username or password')
      return true
    })
    assert.deepEqual(redirects, [])
  })

  await t.test('unauthenticated /me does not reload the login page', async () => {
    status = 401
    await assert.rejects(api.get('/api/v1/me'), ApiError)
    assert.deepEqual(redirects, [])
  })

  await t.test('expired sessions elsewhere still redirect to login', async () => {
    pathname = '/'
    status = 401
    await assert.rejects(api.get('/api/v1/me'), ApiError)
    assert.deepEqual(redirects, ['/login'])
  })

  await t.test('password changes send JSON and CSRF without persisting credentials', async () => {
    const body = { current_password: 'current-password', new_password: 'changed-password' }
    await api.post('/api/v1/me/password', body)
    const request = requests[0]
    assert.equal(new URL(request.url).pathname, '/api/v1/me/password')
    assert.equal(request.headers.get('X-Requested-With'), 'calcside')
    assert.equal(request.headers.get('Authorization'), null)
    assert.deepEqual(await request.json(), body)
  })

  await t.test('an incorrect current password does not redirect away from profile', async () => {
    pathname = '/profile'
    status = 403
    await assert.rejects(api.post('/api/v1/me/password', { current_password: 'wrong', new_password: 'changed-password' }), ApiError)
    assert.deepEqual(redirects, [])
  })

  await t.test('rate-limited login remains on the form', async () => {
    status = 429
    await assert.rejects(api.basicLogin('admin', 'wrong'), (error: unknown) => {
      assert.ok(error instanceof ApiError)
      assert.equal(error.status, 429)
      return true
    })
    assert.deepEqual(redirects, [])
  })
})
