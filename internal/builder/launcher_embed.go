// internal/builder/launcher_embed.go
// Embeds the launcher source files into the molt binary so that `molt build`
// can compile a launcher even when run on a user's machine that doesn't have
// access to the molt source tree. The launcher is a self-contained main
// package (stdlib-only imports) so we can drop the files into a fresh
// directory with a minimal go.mod and `go build .` produces a working binary.
package builder

import "embed"

//go:embed launcher_src/*.go.tpl
var launcherSrc embed.FS

// minimal go.mod sufficient to build the launcher. The launcher imports only
// stdlib so no external dependencies are required.
const launcherGoMod = `module launcher

go 1.21
`
