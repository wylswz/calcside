import { useState } from 'react'
import { Interrupt } from '../api'
import { SwapLabel } from './ui'

// InterruptContext renders the labelled CONTEXT (mono JSON) and
// RESUME URL (mono `POST <url>` + Copy) blocks shared by the Inbox
// detail pane and the run detail waiting pane.
export function InterruptContext({ interrupt: i }: { interrupt: Interrupt }) {
	const [copied, setCopied] = useState(false)
	if (!i.context_json && !i.resume_url) return null
	const copy = async () => {
		try {
			await navigator.clipboard.writeText(i.resume_url ?? '')
			setCopied(true)
			setTimeout(() => setCopied(false), 1500)
		} catch { /* clipboard unavailable */ }
	}
	return (
		<div className="int-ctx">
			{i.context_json && (
				<div className="int-ctx-block">
					<div className="int-ctx-head"><span className="int-ctx-title">Context</span></div>
					<pre className="int-ctx-pre">{i.context_json}</pre>
				</div>
			)}
			{i.resume_url && (
				<div className="int-ctx-block">
					<div className="int-ctx-head">
						<span className="int-ctx-title">Resume URL</span>
						<button type="button" className="text-btn" onClick={() => void copy()}>
							<SwapLabel show={copied ? 'Copied' : 'Copy'} options={['Copy', 'Copied']} />
						</button>
					</div>
					<pre className="int-ctx-pre">POST {i.resume_url}</pre>
				</div>
			)}
		</div>
	)
}
