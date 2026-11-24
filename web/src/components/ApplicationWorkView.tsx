import { forwardRef, type HTMLAttributes, type ReactNode } from 'react'
import '../pages/application.css'

// Shared framed content for the application's work views (Invoke,
// Activity, run detail): a centered column of titled sections that own
// their internal layout — the pane scrolls, sections never shrink.

export function ApplicationWorkView({ children }: { children: ReactNode }) {
  return <div className="app-work-view app-work-content">{children}</div>
}

interface ApplicationWorkSectionProps extends Omit<HTMLAttributes<HTMLElement>, 'title'> {
  title: ReactNode
  description?: ReactNode
  actions?: ReactNode
  children: ReactNode
}

export const ApplicationWorkSection = forwardRef<HTMLElement, ApplicationWorkSectionProps>(
  function ApplicationWorkSection({ title, description, actions, children, className, ...rest }, ref) {
    return (
      <section ref={ref} className={className ? `app-card app-work-section ${className}` : 'app-card app-work-section'} {...rest}>
        <header className="app-work-heading">
          <div className="min-w-0">
            <h2 className="app-card-title">{title}</h2>
            {description && <p className="app-work-desc">{description}</p>}
          </div>
          {actions && <div className="app-work-actions">{actions}</div>}
        </header>
        <div className="app-work-section-body">{children}</div>
      </section>
    )
  },
)
