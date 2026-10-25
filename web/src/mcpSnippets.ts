// mcpSnippets renders the copy-ready MCP client configurations shown
// once with a new restricted key's secret.
export function mcpURL(origin: string): string {
  return `${origin.replace(/\/$/, '')}/mcp`
}

export function claudeCodeSnippet(url: string, key: string): string {
  return `claude mcp add --transport http calcside ${url} --header "Authorization: Bearer ${key}"`
}

export function cursorSnippet(url: string, key: string): string {
  return JSON.stringify({
    mcpServers: {
      calcside: { url, headers: { Authorization: `Bearer ${key}` } },
    },
  }, null, 2)
}

export function headerSnippet(key: string): string {
  return `Authorization: Bearer ${key}`
}
