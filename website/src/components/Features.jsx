import './Features.css'

const features = [
  {
    icon: '🌿',
    title: 'Git Worktree Isolation',
    desc: 'Every task gets its own worktree on a fresh branch. Main is never touched. Bad output = delete branch, not a rollback nightmare.',
  },
  {
    icon: '🚫',
    title: 'Tool Allowlisting',
    desc: 'Explicitly declare which tools the agent may call. Unknown tool names are rejected before execution. No shell exec unless listed.',
  },
  {
    icon: '⏱',
    title: 'Hard Deadlines + Token Budgets',
    desc: 'Wall-clock timeouts and max token counts per task. Killed on breach. Prevents runaway loops and cost blowouts.',
  },
  {
    icon: '🔒',
    title: 'Scoped Secret Injection',
    desc: 'Credentials passed at runtime via env vars scoped to the worktree process. Never baked into the prompt or skill file.',
  },
  {
    icon: '📋',
    title: 'Append-Only Audit Log',
    desc: 'Every tool call with inputs/outputs is logged before execution. Immutable log ready to ship to your SIEM.',
  },
  {
    icon: '🔁',
    title: 'Human-in-the-Loop Gates',
    desc: 'Destructive actions (deploy, DB migration, prod config) require human approval in the issue tracker before proceeding.',
  },
  {
    icon: '🔄',
    title: 'Cleanup, Redispatch & Resume',
    desc: 'Built-in lifecycle management. No duplicate work. Crashed tasks are reconciled by the stale session handler.',
  },
  {
    icon: '📊',
    title: 'Concurrency Limits',
    desc: 'Per-issue resource budgets enforced at the control plane. Prevents agent swarms from overwhelming your infra.',
  },
]

export default function Features() {
  return (
    <section className="features-section" id="features">
      <div className="container">
        <div className="section-label">04 · Guardrails</div>
        <h2 className="section-title">Everything you need.<br />Nothing the agent shouldn't have.</h2>
        <p className="section-subtitle">
          Vigilante's guardrails aren't suggestions — they're enforced at the orchestrator level, not delegated to the model.
        </p>

        <div className="features-grid">
          {features.map((f) => (
            <div key={f.title} className="feature-card">
              <div className="feature-icon">{f.icon}</div>
              <h3 className="feature-title">{f.title}</h3>
              <p className="feature-desc">{f.desc}</p>
            </div>
          ))}
        </div>
      </div>
    </section>
  )
}
