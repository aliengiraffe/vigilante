package skillassets

import "embed"

// Skills contains built-in runtime skill files for installed binaries.
//
//go:embed skills/vigilante-issue-implementation skills/vigilante-issue-implementation-on-monorepo skills/vigilante-issue-implementation-on-turborepo skills/vigilante-issue-implementation-on-nx skills/vigilante-issue-implementation-on-rush skills/vigilante-issue-implementation-on-rush-monorepo skills/vigilante-issue-implementation-on-bazel skills/vigilante-issue-implementation-on-gradle skills/vigilante-issue-implementation-on-gradle-multi-project skills/vigilante-issue-implementation-on-bazel-monorepo skills/vigilante-conflict-resolution skills/vigilante-create-issue skills/vigilante-local-service-dependencies skills/docker-compose-launch
var Skills embed.FS
