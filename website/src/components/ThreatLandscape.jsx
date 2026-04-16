import './ThreatLandscape.css'

const threats = [
  {
    label: 'Prompt Injection',
    cve: 'CVE-2025-32711',
    desc: 'EchoLeak: crafted emails hijack M365 Copilot with zero clicks. Devin AI found fully exposed — leaked tokens via crafted prompts.',
    color: '#e63030',
  },
  {
    label: 'Credential Exfiltration',
    cve: 'GitGuardian 2026',
    desc: '24,008 secrets found in MCP config files. litellm 1.82.7 silently collected SSH keys, AWS creds, K8s secrets — shipped AES-encrypted to attacker C2.',
    color: '#e63030',
  },
  {
    label: 'Supply Chain via npm',
    cve: 'Axios · 83M/week',
    desc: 'Compromised maintainer account pushed a RAT dropper. Cross-platform C2 with self-deleting forensic cleanup. 83M weekly downloads affected.',
    color: '#e63030',
  },
  {
    label: 'Privilege Escalation',
    cve: 'Snowflake Cortex 2025',
    desc: 'README prompt injection bypassed human-in-the-loop approval, disabled its own sandbox, and executed malware via shell process substitution.',
    color: '#e63030',
  },
  {
    label: 'Token & OAuth Abuse',
    cve: 'Apr 2026',
    desc: 'ShinyHunters stole long-lived integration tokens from an AI analytics provider, laterally accessing 12+ enterprise environments. MFA useless against M2M tokens.',
    color: '#e63030',
  },
  {
    label: 'Agent Framework RCE',
    cve: 'CVE-2025-34291',
    desc: 'Langflow: CORS misconfiguration + missing CSRF = full RCE in one page visit. Exposed all stored API keys. Actively exploited by Flodric botnet.',
    color: '#e63030',
  },
]

export default function ThreatLandscape() {
  return (
    <section className="threats-section" id="threats">
      <div className="container">
        <div className="section-label">01 · Threat Landscape</div>
        <h2 className="section-title">Coding agents are getting <span className="red-text">owned</span></h2>
        <p className="section-subtitle">
          Real CVEs from the past 12 months. These aren't theoretical — each one hit production systems. The attack surface expands every time you hand an agent a credential.
        </p>

        <div className="threats-grid">
          {threats.map((t) => (
            <div key={t.label} className="threat-card">
              <div className="threat-header">
                <span className="threat-label">{t.label}</span>
                <span className="threat-cve mono">{t.cve}</span>
              </div>
              <p className="threat-desc">{t.desc}</p>
            </div>
          ))}
        </div>

        <div className="threats-callout">
          <div className="callout-icon">⚠</div>
          <p>
            <strong>The root cause is the same every time:</strong> agents run with ambient credentials, unbounded tool access, and no enforced isolation. Vigilante eliminates all three.
          </p>
        </div>
      </div>
    </section>
  )
}
