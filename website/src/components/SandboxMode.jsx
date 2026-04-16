import './SandboxMode.css'

export default function SandboxMode() {
  return (
    <section className="sandbox-section" id="sandbox">
      <div className="container">
        <div className="section-label">05b · Sandbox Mode</div>
        <h2 className="section-title">Two isolation layers.<br />Zero host credential exposure.</h2>
        <p className="section-subtitle">
          Sandbox mode adds a Docker container on top of git worktree isolation. The agent never sees your host credentials — ever.
        </p>

        <div className="sandbox-layout">
          <div className="layers-col">
            <div className="layer-card layer-always">
              <div className="layer-badge mono">Layer 1 · Always On</div>
              <h3>Git Worktree</h3>
              <p>Isolated branch per issue. Main checkout untouched. Delete the branch to undo anything.</p>
            </div>
            <div className="layer-plus">+</div>
            <div className="layer-card layer-sandbox">
              <div className="layer-badge mono">Layer 2 · Sandbox Mode</div>
              <h3>Docker Container</h3>
              <p>Agent runs inside container. Host credentials are never shared. Full process isolation.</p>
            </div>
          </div>

          <div className="flow-col">
            <div className="flow-title mono">CLI mirroring layer — gh today, more adapters to come</div>
            <div className="flow-chain">
              <div className="flow-node">Agent</div>
              <div className="flow-arrow">→</div>
              <div className="flow-node">CLI mirror<br/><span className="flow-sub">(gh first, in container)</span></div>
              <div className="flow-arrow">→</div>
              <div className="flow-node">Reverse Proxy<br/><span className="flow-sub">(host)</span></div>
              <div className="flow-arrow">→</div>
              <div className="flow-node">GitHub API</div>
            </div>
            <div className="flow-note mono">Token validated: signature + session + repo scope + TTL</div>

            <div className="creds-grid">
              <div className="cred-item">
                <div className="cred-label">HMAC session token</div>
                <div className="cred-desc">Scoped to 1 repo + session TTL (2h default)</div>
              </div>
              <div className="cred-item">
                <div className="cred-label">Ephemeral SSH deploy key</div>
                <div className="cred-desc">Registered on GitHub, revoked at teardown</div>
              </div>
              <div className="cred-item">
                <div className="cred-label">VIGILANTE_PROXY_URL</div>
                <div className="cred-desc">Bound to 127.0.0.1, per-session port</div>
              </div>
            </div>
          </div>
        </div>

        <div className="can-cannot">
          <div className="can-block">
            <div className="can-header green-text">✓ Agent CAN</div>
            <ul>
              <li>Read/write assigned repo</li>
              <li>Run build, test, lint toolchains</li>
              <li>Post comments &amp; push commits</li>
              <li>Start services via Docker-in-Docker</li>
            </ul>
          </div>
          <div className="cannot-block">
            <div className="can-header red-text">✗ Agent CANNOT</div>
            <ul>
              <li>Access host files or credentials</li>
              <li>Touch any other repository</li>
              <li>Use host gh identity directly</li>
              <li>Extend its own credential TTL</li>
            </ul>
          </div>
        </div>

        <div className="teardown-note mono">
          Guaranteed teardown: token invalidation → deploy key revocation → container removal. Stale session reconciler handles crashes.
        </div>
      </div>
    </section>
  )
}
