import { useEffect, useState } from 'react'

const ICON_RE = /^\/api\/v1\/extensions\/icons\/[a-f0-9]{64}$/

// ExtensionIcon renders a packaged same-origin catalog icon with a puzzle
// fallback. It refuses external URLs outright — the only legal src is the
// authenticated catalog route, so nothing can exfiltrate or fabricate brand.
export function ExtensionIcon({ iconUrl, name }: { iconUrl?: string; name: string }) {
  const valid = !!iconUrl && ICON_RE.test(iconUrl)
  const [broken, setBroken] = useState(false)
  useEffect(() => { setBroken(false) }, [iconUrl])
  if (!valid || broken) {
    return (
      <svg aria-hidden="true" width="32" height="32" viewBox="0 0 24 24" fill="none"
        stroke="currentColor" strokeWidth="1.5" strokeLinecap="round" strokeLinejoin="round" className="ext-icon ext-icon-fallback">
        <path d="M4 7h3a1 1 0 0 0 1-1V5a2 2 0 0 1 4 0v1a1 1 0 0 0 1 1h3a1 1 0 0 1 1 1v3a1 1 0 0 0 1 1h1a2 2 0 0 1 0 4h-1a1 1 0 0 0-1 1v3a1 1 0 0 1-1 1h-3a1 1 0 0 1-1-1v-1a2 2 0 0 0-4 0v1a1 1 0 0 1-1 1H4a1 1 0 0 1-1-1v-3a1 1 0 0 0-1-1v-1a2 2 0 0 0 0-4v-1a1 1 0 0 0 1-1V8a1 1 0 0 1 1-1z" />
        <title>{name}</title>
      </svg>
    )
  }
  return <img src={iconUrl} alt="" aria-hidden="true" width="32" height="32" loading="lazy" className="ext-icon" onError={() => setBroken(true)} />
}
