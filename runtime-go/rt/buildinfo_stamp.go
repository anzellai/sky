package rt

// The build identity `sky build` resolves (version, commit, built-at, and where
// the commit came from). It reaches the binary through GENERATED GO SOURCE: the
// compiler writes a `skybuildinfo` package into the emitted tree (linked
// by a blank import in `sky_buildinfo.go` beside `main.go`) whose `init` calls
// SetBuildStamp. Any `go build` of the emitted tree therefore carries it — the
// one `sky build` runs, a manual cross-compile, a Dockerfile, a custom CI.
//
// v0.25.20 passed these values only as `-X` linker flags on the `go build`
// that `sky build` ran itself, so an app whose deploy cross-compiled
// `sky-out/` with its own plain `go build` reported dev/unknown/dev.
//
// Precedence (observability.go, currentBuildInfo): a user's own
// `-ldflags "-X sky-app/rt.buildCommit=..."` (and buildAt / skyVersion) wins,
// field by field, over this stamp; the stamp wins over the dev defaults.
//
// No build tag: the wasm client links the same generated package.

type buildStamp struct {
	version string
	commit  string
	builtAt string
	source  string
}

var embeddedStamp buildStamp

// SetBuildStamp records the build identity from the generated
// `skybuildinfo` package. Called once, from its `init`, before `main`.
func SetBuildStamp(version, commit, builtAt, source string) {
	embeddedStamp = buildStamp{version: version, commit: commit, builtAt: builtAt, source: source}
}
