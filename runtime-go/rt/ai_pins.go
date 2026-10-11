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
