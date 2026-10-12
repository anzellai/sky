//go:build !js

package rt

// ai_pins.go — the pinned artefacts Std.Ai.Local fetches.
//
// Every engine build and every default model is pinned by a version and a
// SHA-256 recorded here, in the repository. A fetched file whose digest does
// not match installs nothing (ai_fetch.go). Changing a pin is a reviewed
// edit of this file, and the CI job that caches the AI artefacts keys its
// cache on this file's hash.
//
// The engine builds are the upstream llama.cpp GitHub release assets. A
// later task publishes Sky's own bundles (with SBOMs) through a workflow like
// postgres-bundle.yml; the pins then move to those assets.

import "strings"

// aiLlamaBuild is the pinned llama.cpp release tag.
const aiLlamaBuild = "b11200"

// aiLlamaAsset is one pinned engine archive for one host.
type aiLlamaAsset struct {
	goos, goarch string
	variant      string // "metal" or "cpu": the accelerator the build drives
	file         string // the release asset file name
	sha256       string
}

// aiLlamaAssets lists the pinned engine archives. A host with no entry has no
// managed engine: Std.Ai.Local fails with NotProvisioned and names
// <PREFIX>_AI_LLAMA_BIN, the way to bring an operator's own build.
var aiLlamaAssets = []aiLlamaAsset{
	{
		goos: "darwin", goarch: "arm64", variant: "metal",
		file:   "llama-b11200-bin-macos-arm64.tar.gz",
		sha256: "caa654dcae608fa2a3047da4fccbe3c8a1aab1dcc3832313286024cb13182124",
	},
	{
		goos: "linux", goarch: "amd64", variant: "cpu",
		file:   "llama-b11200-bin-ubuntu-x64.tar.gz",
		sha256: "d6fbb47c57ce3495ca246b21010e1c46223f3f501b8465cd0b47139888b9f1fe",
	},
}

// aiLlamaReleaseURL is where a pinned asset is downloaded from. A variable so
// the fetch tests can point it at a local server.
var aiLlamaReleaseURL = func(file string) string {
	return "https://github.com/ggml-org/llama.cpp/releases/download/" + aiLlamaBuild + "/" + file
}

// aiHuggingFaceURL resolves a pinned Hugging Face file. A variable so the
// fetch tests can point it at a local server.
var aiHuggingFaceURL = func(repo, file string) string {
	return "https://huggingface.co/" + repo + "/resolve/main/" + file
}

// The default development model: Qwen2.5 0.5B Instruct (Apache-2.0), Q4_0.
// It is the smallest Qwen build that answers a chat and serves embeddings on
// a laptop CPU, so CI and the examples use it too. Std.Ai.Local exposes it as
// `Local.devModel`; the Sky source carries the same three values.
const (
	aiDevModelRepo   = "Qwen/Qwen2.5-0.5B-Instruct-GGUF"
	aiDevModelFile   = "qwen2.5-0.5b-instruct-q4_0.gguf"
	aiDevModelSha256 = "7671c0c304e6ce5a7fc577bcb12aba01e2c155cc2efd29b2213c95b18edaf6ed"
)

// ─── the training toolchains (Std.Ai.Train) ─────────────────────────────
//
// A training toolchain is a managed Python: the pinned uv, the pinned CPython
// build from python-build-standalone, and a virtual environment installed from
// a hash lock (ai_train_assets/locks). The system Python is never used.

// aiToolAsset is one pinned archive of a tool for one host.
type aiToolAsset struct {
	goos, goarch string
	target       string // the tool's own platform name
	file         string
	sha256       string
}

// aiUvVersion is the pinned uv release.
const aiUvVersion = "0.12.16"

var aiUvAssets = []aiToolAsset{
	{goos: "darwin", goarch: "arm64", target: "aarch64-apple-darwin", file: "uv-aarch64-apple-darwin.tar.gz",
		sha256: "b6e03fae61704b1aa622f12b792a69483e837b83068e44f4fd34f8a07a8f74a3"},
	{goos: "linux", goarch: "amd64", target: "x86_64-unknown-linux-gnu", file: "uv-x86_64-unknown-linux-gnu.tar.gz",
		sha256: "8e5c6e5523dffc2dcf615bd995554c84c9feb4e577808a3fb8698a639d3f8d9c"},
	{goos: "linux", goarch: "arm64", target: "aarch64-unknown-linux-gnu", file: "uv-aarch64-unknown-linux-gnu.tar.gz",
		sha256: "36d913ee9c647481d64f1a0a0485f85ff2feaee605c341fc22e73398f9212c26"},
}

// aiUvReleaseURL is where a uv archive is downloaded from. A variable so the
// toolchain tests can point it at a local server.
var aiUvReleaseURL = func(file string) string {
	return "https://github.com/astral-sh/uv/releases/download/" + aiUvVersion + "/" + file
}

// aiPythonTag and aiPythonVersion pin the CPython build (python-build-standalone,
// install_only_stripped: the interpreter and the standard library, no tests).
const (
	aiPythonTag     = "20260924"
	aiPythonVersion = "3.12.14"
)

var aiPythonAssets = []aiToolAsset{
	{goos: "darwin", goarch: "arm64", target: "aarch64-apple-darwin",
		file:   "cpython-3.12.14+20260924-aarch64-apple-darwin-install_only_stripped.tar.gz",
		sha256: "c2edb321cd32ec2b170df208db0446dccc4398db602ca27cf2079098fb1f7d9d"},
	{goos: "linux", goarch: "amd64", target: "x86_64-unknown-linux-gnu",
		file:   "cpython-3.12.14+20260924-x86_64-unknown-linux-gnu-install_only_stripped.tar.gz",
		sha256: "269b2c99e4db15b242bf01832f4fea1e8f1a664f273cff519393f296e9820b41"},
	{goos: "linux", goarch: "arm64", target: "aarch64-unknown-linux-gnu",
		file:   "cpython-3.12.14+20260924-aarch64-unknown-linux-gnu-install_only_stripped.tar.gz",
		sha256: "c8499b61252c433280f134df954464d19811527b31cb920c35fc6967c1222e35"},
}

// aiPythonReleaseURL is where a CPython archive is downloaded from (the `+`
// in the file name is escaped). A variable for the tests.
var aiPythonReleaseURL = func(file string) string {
	return "https://github.com/astral-sh/python-build-standalone/releases/download/" + aiPythonTag + "/" +
		strings.ReplaceAll(file, "+", "%2B")
}

// aiHuggingFaceRevisionURL resolves one file of a pinned base snapshot at its
// commit. A variable for the tests.
var aiHuggingFaceRevisionURL = func(repo, revision, file string) string {
	return "https://huggingface.co/" + repo + "/resolve/" + revision + "/" + file
}

// aiPyPIIndex is the package index the toolchains install from. Every wheel
// is pinned by its SHA-256 in the lock, so the index is trusted for
// availability only, never for content.
const aiPyPIIndex = "https://pypi.org/simple"
