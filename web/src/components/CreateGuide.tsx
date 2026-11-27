export type CreateKind = 'instance' | 'application'

// Creating has one kind for the whole flow: whatever entry point you
// came from. The guide is a compact text note beside the basics form;
// there is deliberately no Instance/Application switch here.
export function CreateGuide({ kind }: { kind: CreateKind }) {
  const instance = kind === 'instance'
  return (
    <aside className="create-guide" aria-label={instance ? 'About instances' : 'About applications'}>
      <article className="create-guide-card" aria-labelledby={`create-guide-${kind}-title`}>
        <div className="create-guide-top">
          <span className="create-guide-eyebrow">
            <span className={`create-guide-dot ${instance ? 'is-live' : 'is-accent'}`} aria-hidden="true" />
            {instance ? 'Playground' : 'API service'}
          </span>
          <span className="create-guide-tag">{instance ? 'persistent · stateful' : 'versioned · stateless'}</span>
        </div>
        <h2 id={`create-guide-${kind}-title`} className="create-guide-title">{instance ? 'Instance' : 'Application'}</h2>
        <p className="create-guide-lede">
          {instance
            ? 'A live workbench that stays open while you explore. An agent connects over MCP and calls it again and again — every run shares the same memory and files.'
            : 'A durable API you publish, version and call. Callers send JSON to main(input) and get JSON back — each call starts from a fresh session.'}
        </p>
        <ul className="create-guide-points">
          {instance ? (
            <>
              <li>Globals and files persist between runs; the process stays warm.</li>
              <li>Best for exploring, debugging and long-running work.</li>
              <li>Every call shows up in the run history.</li>
            </>
          ) : (
            <>
              <li>Publish a version when the code is ready to serve.</li>
              <li>Input/output schemas pin the JSON contract.</li>
              <li>Each call is an independent run with its own audit trail.</li>
              <li>API keys decide who can call it.</li>
            </>
          )}
        </ul>
      </article>
    </aside>
  )
}
