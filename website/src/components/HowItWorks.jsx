import './HowItWorks.css'

const steps = [
  {
    num: '01',
    title: 'Issue Tracker',
    desc: 'GitHub Issues act as the work queue. Label-gated eligibility filter — only issues tagged "vigilante" are picked up.',
    icon: '📋',
  },
  {
    num: '02',
    title: 'Control Plane',
    desc: 'Schedules, rate-limits, and deduplicates. Enforces concurrency caps so your infra doesn\'t get overwhelmed.',
    icon: '⚙',
  },
  {
    num: '03',
    title: 'Isolated Worktree',
    desc: 'One fresh git worktree per issue. Main checkout is always untouched. Scope never bleeds between tasks.',
    icon: '🌿',
  },
  {
    num: '04',
    title: 'Coding Agent CLI',
    desc: 'Any headless agent — codex, claude, gemini. Runs scoped to the worktree only. No ambient host access.',
    icon: '🤖',
  },
  {
    num: '05',
    title: 'PR + Audit Log',
    desc: 'Opens a PR to upstream. Logs every step: issue → branch → diff → PR. Full trace for forensics.',
    icon: '📊',
  },
]

export default function HowItWorks() {
  return (
    <section className="how-section" id="how-it-works">
      <div className="container">
        <div className="section-label">05 · Architecture</div>
        <h2 className="section-title">How Vigilante works</h2>
        <p className="section-subtitle">
          A deterministic pipeline from issue to PR. The model is untrusted by default — the orchestrator enforces policy, not the agent.
        </p>

        <div className="pipeline">
          {steps.map((step, i) => (
            <div key={step.num} className="pipeline-item">
              <div className="pipeline-card">
                <div className="pipeline-icon">{step.icon}</div>
                <div className="pipeline-num mono">{step.num}</div>
                <h3 className="pipeline-title">{step.title}</h3>
                <p className="pipeline-desc">{step.desc}</p>
              </div>
              {i < steps.length - 1 && (
                <div className="pipeline-arrow">
                  <svg width="24" height="24" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2"><path d="M5 12h14M12 5l7 7-7 7"/></svg>
                </div>
              )}
            </div>
          ))}
        </div>

        <div className="key-insight">
          <div className="insight-label mono">Key Insight</div>
          <p>The model is untrusted by default. The orchestrator enforces policy — not the model.</p>
        </div>
      </div>
    </section>
  )
}
