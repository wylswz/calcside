// applicationSnippets produces copy-ready invocation examples for a
// published (or publish-pending) application. The raw input text is
// embedded verbatim — never re-parsed — so precision is preserved.

function shellQuote(s: string): string {
  return `'${s.replace(/'/g, `'"'"'`)}'`
}

export function applicationSnippets(origin: string, id: string, inputJSON: string): {
  curl: string
  python: string
  javascript: string
} {
  const url = `${origin}/api/v1/applications/${encodeURIComponent(id)}/invoke`
  const body = inputJSON.trim() ? inputJSON : '{}'
  return {
    curl: `curl --request POST \\
  --url ${shellQuote(url)} \\
  --header 'Content-Type: application/json' \\
  --header "Authorization: Bearer \${CALCSIDE_API_KEY:?Set CALCSIDE_API_KEY}" \\
  --fail-with-body \\
  --data-raw ${shellQuote(body)}`,
    python: `import json
import os
import urllib.request

req = urllib.request.Request(
    ${JSON.stringify(url)},
    data=${JSON.stringify(body)}.encode('utf-8'),
    headers={
        'Content-Type': 'application/json',
        'Authorization': 'Bearer ' + os.environ['CALCSIDE_API_KEY'],
    },
    method='POST',
)
with urllib.request.urlopen(req) as resp:
    body = json.load(resp)
if body.get('result', {}).get('error'):
    raise RuntimeError(body['result']['error'])
# output_json is verbatim text — precision stays intact
print(body.get('output_json', body))
`,
    javascript: `// Node 18+ — built-in fetch, no dependencies
const apiKey = process.env.CALCSIDE_API_KEY
if (!apiKey) throw new Error('Set CALCSIDE_API_KEY')
const res = await fetch(${JSON.stringify(url)}, {
  method: 'POST',
  headers: {
    'Content-Type': 'application/json',
    'Authorization': 'Bearer ' + apiKey,
  },
  body: ${JSON.stringify(body)},
})
if (!res.ok) throw new Error('invoke failed: ' + res.status)
const body = await res.json()
if (body.result && body.result.error) throw new Error(JSON.stringify(body.result.error))
// output_json is verbatim text — precision stays intact
console.log(body.output_json ?? body)
`,
  }
}
