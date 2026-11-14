import { defineConfig } from 'vite'
import react from '@vitejs/plugin-react'

// Backend target for the Vite dev proxy (default: `make dev` backend).
const apiTarget = process.env.VITE_API_TARGET || 'http://127.0.0.1:8787'

export default defineConfig({
  plugins: [react()],
  server: {
    host: '127.0.0.1',
    proxy: {
      '/api': apiTarget,
      '/mcp': apiTarget,
      '/auth': apiTarget,
    },
  },
  build: {
    // Outside the Vite root: the Go module embeds the output from
    // server/webdist (see server/webdist/embed.go).
    outDir: '../server/webdist/dist',
    emptyOutDir: true,
  },
})
