import { forwardRef, type HTMLAttributes, type ReactNode } from 'react'
import './detailWorkbench.css'
import '../pages/application.css'

// Shared workbench primitives for application work views (Invoke,
// Activity, run detail): the same IDE vocabulary as the notebook —
// hairline-separated panes with small uppercase heads inside a
// viewport-filling .wb-view.

export function WorkView({ children, scroll }: { children: ReactNode; scroll?: boolean }) {
  return <div className={scroll ? 'wb-view wb-view-scroll' : 'wb-view'}>{children}</div>
}

export function WorkToolbar({ children }: { children: ReactNode }) {
  return <div className="wb-toolbar">{children}</div>
}

export function WorkStatusbar({ children }: { children: ReactNode }) {
  return <div className="wb-statusbar">{children}</div>
}

// Shared status → dot tone for applications, runs, and steps.
import { semanticToneOf, type SemanticTone } from '../ui/core'
export type StatusTone = SemanticTone
export function statusTone(status: string): StatusTone {
  return semanticToneOf(status)
}

export function StatusCell({ status, suffix }: { status: string; suffix?: ReactNode }) {
  return (
    <span className="wb-status-cell">
      <span className="wb-dot" data-tone={statusTone(status)} />
      {status}{suffix}
    </span>
  )
}

interface WorkPaneProps extends Omit<HTMLAttributes<HTMLElement>, 'title'> {
  title: ReactNode
  note?: ReactNode
  actions?: ReactNode
  /** extra class on the scrollable body, e.g. 'wb-grow' overrides */
  bodyClassName?: string
  children: ReactNode
}

export const WorkPane = forwardRef<HTMLElement, WorkPaneProps>(
  function WorkPane({ title, note, actions, bodyClassName, children, className, ...rest }, ref) {
    return (
      <section ref={ref} className={className ? `wb-pane ${className}` : 'wb-pane'} {...rest}>
        <header className="wb-pane-head">
          <h3 className="wb-pane-title">{title}</h3>
          {note && <span className="wb-pane-note">{note}</span>}
          {actions && <div className="wb-pane-actions flex items-center gap-2">{actions}</div>}
        </header>
        <div className={bodyClassName ? `wb-pane-body ${bodyClassName}` : 'wb-pane-body'}>{children}</div>
      </section>
    )
  },
)
