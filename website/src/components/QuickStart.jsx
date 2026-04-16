import './QuickStart.css'

const steps = [
  { num: '1', label: 'Install', cmd: 'brew install vigilante' },
  { num: '2', label: 'Bootstrap', cmd: 'vigilante setup --provider codex' },
  { num: '3', label: 'Register repo', cmd: 'vigilante watch ~/my-service' },
  { num: '4', label: 'Trigger', cmd: "# Label a GitHub issue 'vigilante'" },
  { num: '5', label: 'Observe', cmd: 'vigilante status && vigilante logs' },
]

const tldr = [
  { n: '1', text: 'Agents are untrusted code. Treat them accordingly — not as trusted engineers.' },
  { n: '2', text: "Orchestrators enforce policy. The model doesn't. Never let the model self-authorize." },
  { n: '3', text: 'Ephemeral = safe. Persistent = risky. Scope every task to a fresh, throwaway environment.' },
  { n: '4', text: 'Worktrees are your friend. Isolation at the git level is cheap and effective.' },
  { n: '5', text: 'Auth is broken for agents today. Short-lived, scoped creds are the only viable patch.' },
]

export default function QuickStart() {
  return (
    <section className="qs-section" id="quickstart">
      <div className="container">
        <div className="qs-split">
          <div className="qs-left">
            <div className="section-label">07 · Demo</div>
            <h2 className="section-title">Up in 5 commands.</h2>
            <p className="section-subtitle">
              Single Go binary. No daemon, no sidecar, no YAML sprawl.
            </p>

            <div className="steps-list">
              {steps.map((s) => (
                <div key={s.num} className="qs-step">
                  <div className="qs-step-num mono">{s.num}</div>
                  <div className="qs-step-content">
                    <div className="qs-step-label">{s.label}</div>
                    <div className="qs-cmd mono">{s.cmd}</div>
                  </div>
                </div>
              ))}
            </div>

            <div className="watch-note">
              <span className="watch-label">Watch for:</span>
              <div className="watch-tags">
                <span>worktree created</span>
                <span>agent scoped to branch</span>
                <span>PR opened</span>
                <span>issue updated</span>
                <span>cleanup on completion</span>
              </div>
            </div>
          </div>

          <div className="qs-right">
            <div className="section-label">TL;DR</div>
            <h2 className="section-title">Six things<br/>to internalize.</h2>
            <div className="tldr-list">
              {tldr.map((t) => (
                <div key={t.n} className="tldr-item">
                  <div className="tldr-num mono">{t.n}</div>
                  <p>{t.text}</p>
                </div>
              ))}
              <div className="tldr-item tldr-final">
                <div className="tldr-num mono">6</div>
                <p>Vigilante gives you all of this — <strong>free, open source, Go binary.</strong> Ship it.</p>
              </div>
            </div>
          </div>
        </div>
      </div>
    </section>
  )
}
