package skillassets

import "embed"

// Skills contains built-in runtime skill files for installed binaries.
//
//go:embed skills/vigilante-issue-implementation skills/vigilante-issue-implementation-on-monorepo skills/vigilante-conflict-resolution skills/vigilante-create-issue
var Skills embed.FS
