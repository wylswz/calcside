import type { ReactNode } from 'react'
import { Link } from 'react-router-dom'
import './detailWorkbench.css'

// Shared IDE-style detail frame: the instance and application routes
// paint the same compact identity header and view tab strip above
// viewport-filling panes that own their scrollers.

export function DetailWorkbenchHeader({ backTo, backLabel, title, id, description, status, actions }: {
  backTo: string
  backLabel: string
  title: string
  id: string
  description?: string
  status: ReactNode
  actions?: ReactNode
}) {
  return (
    <header className="detail-head">
      <div className="detail-id min-w-0 flex-1">
        <div className="detail-crumb text-xs text-sec"><Link to={backTo} className="transition-colors hover:text-accent">{backLabel}</Link></div>
        <h1 className="break-words font-semibold text-ink" title={title}>{title}</h1>
        {title !== id && <div className="mt-1 truncate font-mono text-xs text-mute" title={id}>{id}</div>}
        {description && <p className="detail-desc text-sm text-sec" title={description}>{description}</p>}
        <div className="detail-header-status mt-2 flex items-center gap-3">{status}</div>
      </div>
      {actions && <div className="detail-actions ml-auto flex items-center gap-2">{actions}</div>}
    </header>
  )
}

export function DetailWorkbenchViews({ children, actions }: { children: ReactNode; actions?: ReactNode }) {
  return <div className="detail-views">{children}{actions}</div>
}
