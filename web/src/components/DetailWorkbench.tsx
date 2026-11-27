import type { ReactNode } from 'react'
import { Link } from 'react-router-dom'
import './detailWorkbench.css'

// Shared IDE-style detail frame: one compact identity row (crumb ·
// title · id · status · actions) and a view tab strip above
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
      <Link to={backTo} className="detail-crumb">{backLabel}</Link>
      <h1 className="detail-title" title={title}>{title}</h1>
      {title !== id && <code className="detail-id" title={id}>{id}</code>}
      {description && <span className="detail-desc" title={description}>{description}</span>}
      <div className="detail-header-status">{status}</div>
      {actions && <div className="detail-actions">{actions}</div>}
    </header>
  )
}

export function DetailWorkbenchViews({ children, actions }: { children: ReactNode; actions?: ReactNode }) {
  return <div className="detail-views">{children}{actions}</div>
}
