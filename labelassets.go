package skillassets

import "embed"

// LabelsManifest contains the canonical Vigilante repository label definitions.
//
//go:embed .github/labels.json
var LabelsManifest embed.FS
