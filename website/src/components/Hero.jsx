import './Hero.css'

export default function Hero() {
  return (
    <section className="hero-section">
      <div className="hero-grid-bg" />
      <div className="hero-glow" />
      <div className="container hero-container">
        <nav className="nav">
          <span className="nav-wordmark">VIGILANTE</span>
          <div className="nav-links">
            <a href="#how-it-works">How it works</a>
            <a href="#features">Features</a>
            <a href="#sandbox">Sandbox</a>
            <a href="https://github.com/aliengiraffe/vigilante" target="_blank" className="nav-github">
              <svg width="18" height="18" viewBox="0 0 24 24" fill="currentColor"><path d="M12 0C5.374 0 0 5.373 0 12c0 5.302 3.438 9.8 8.207 11.387.599.111.793-.261.793-.577v-2.234c-3.338.726-4.033-1.416-4.033-1.416-.546-1.387-1.333-1.756-1.333-1.756-1.089-.745.083-.729.083-.729 1.205.084 1.839 1.237 1.839 1.237 1.07 1.834 2.807 1.304 3.492.997.107-.775.418-1.305.762-1.604-2.665-.305-5.467-1.334-5.467-5.931 0-1.311.469-2.381 1.236-3.221-.124-.303-.535-1.524.117-3.176 0 0 1.008-.322 3.301 1.23A11.509 11.509 0 0112 5.803c1.02.005 2.047.138 3.006.404 2.291-1.552 3.297-1.23 3.297-1.23.653 1.653.242 2.874.118 3.176.77.84 1.235 1.911 1.235 3.221 0 4.609-2.807 5.624-5.479 5.921.43.372.823 1.102.823 2.222v3.293c0 .319.192.694.801.576C20.566 21.797 24 17.3 24 12c0-6.627-5.373-12-12-12z"/></svg>
              GitHub
            </a>
          </div>
        </nav>

        <div className="hero-content">
          <div className="hero-badge">
            <span className="badge-dot" />
            Open Source · Go Binary · Apache 2.0
          </div>

          <h1 className="hero-title">
            Coding agents need<br />
            <span className="hero-accent">a warden,</span><br />
            not a wishlist.
          </h1>

          <p className="hero-desc">
            Vigilante is a sandbox-first orchestration layer for coding agents. It isolates every task in a git worktree, enforces strict credential scoping, and gives you full audit logs — so your agents can't burn down production.
          </p>

          <div className="hero-ctas">
            <a href="#quickstart" className="cta-primary">
              Get started
              <svg width="16" height="16" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2.5"><path d="M5 12h14M12 5l7 7-7 7"/></svg>
            </a>
            <a href="https://github.com/aliengiraffe/vigilante" target="_blank" className="cta-secondary">
              View on GitHub
            </a>
          </div>

          <div className="hero-install">
            <span className="mono install-cmd">brew install vigilante</span>
          </div>
        </div>

        <div className="hero-visual">
          <img src="/logo.png" alt="Vigilante" className="hero-logo-big" />
        </div>
      </div>

      <div className="hero-stats">
        <div className="container stats-row">
          <div className="stat">
            <span className="stat-val">1 worktree</span>
            <span className="stat-label">per issue — no scope bleed</span>
          </div>
          <div className="stat-divider" />
          <div className="stat">
            <span className="stat-val">0 host creds</span>
            <span className="stat-label">shared with agent in sandbox mode</span>
          </div>
          <div className="stat-divider" />
          <div className="stat">
            <span className="stat-val">Full audit log</span>
            <span className="stat-label">every tool call, every diff</span>
          </div>
          <div className="stat-divider" />
          <div className="stat">
            <span className="stat-val">codex · claude · gemini</span>
            <span className="stat-label">any headless agent CLI</span>
          </div>
        </div>
      </div>
    </section>
  )
}
