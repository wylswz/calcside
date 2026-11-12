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

// Devin cloud takes the same URL and bearer header through its
// Customize → MCPs form; Devin CLI reads this mcpServers JSON from
// .devin/mcp_config.json (or ~/.config/devin/mcp_config.json).
export function devinSnippet(url: string, key: string): string {
  return JSON.stringify({
    mcpServers: {
      calcside: {
        url,
        transport: 'http',
        headers: { Authorization: `Bearer ${key}` },
      },
    },
  }, null, 2)
}

// OpenCode reads this from opencode.json(c) in the project (or the
// global config); oauth is disabled because the key is a bearer header.
export function opencodeSnippet(url: string, key: string): string {
  return JSON.stringify({
    $schema: 'https://opencode.ai/config.json',
    mcp: {
      servers: {
        calcside: {
          type: 'remote',
          url,
          oauth: false,
          headers: { Authorization: `Bearer ${key}` },
        },
      },
    },
  }, null, 2)
}

export function headerSnippet(key: string): string {
  return `Authorization: Bearer ${key}`
}
