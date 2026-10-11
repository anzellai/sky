//go:build !js

package rt

// ai_local.go — the Std.Ai.Local kernels.
//
//	Ai_localFit : RawSpec -> Task Error RawFit
//	Ai_localStart : RawSpec -> Task Error Int
//	Ai_localPost : Int -> String -> String -> Task Error String
//	Ai_localStatus : Int -> Task Error Status
//	Ai_localStop : Int -> Task Error ()
//	Ai_localFetch : String -> Task Error String
//	Ai_localPull : String -> String -> Task Error Pulled
//	Ai_localProvision : () -> Task Error Provisioned
//	Ai_localEngines : () -> Task Error (List EngineInfo)
//	Ai_localStopSlot : String -> Task Error Int
//
// A spec given to Local.start directly has no slot, so no environment layer:
// the code is the whole configuration. A slot (Std.Ai.Endpoint) adds the
// `<PREFIX>_AI_<SLOT>_*` layer over the same spec.

import (
	"context"
	"runtime"
	"strings"
	"time"
)

// aiLocalCallTimeout bounds one call to an engine started with Local.start.
const aiLocalCallTimeout = 120 * time.Second

func aiResolveDirect(specArg any) (aiLocalSpec, error) {
	return resolveAiLocal("", parseAiLocalBuilder(specArg), "", func(aiSetting) {})
}

// aiFitVariant is the accelerator variant a fit plans for without fetching
// the engine: an operator build is "custom", else the pinned build's variant.
func aiFitVariant() string {
	if strings.TrimSpace(skyGetenv("AI_LLAMA_BIN")) != "" {
		return "custom"
	}
	if a, ok := aiHostAsset(); ok {
		return a.variant
	}
	return "cpu"
}

func aiFitValue(f aiFit) any {
	return map[string]any{
		"accelerator":             f.accelerator,
		"weightsBytes":            int(f.weightsBytes),
		"kvCacheBytes":            int(f.kvCacheBytes),
		"availableBytes":          int(f.availableBytes),
		"gpuLayers":               f.gpuLayers,
		"contextTokens":           f.contextTokens,
		"expectedTokensPerSecond": f.expectedTps,
	}
}

// Ai_localFit : RawSpec -> Task Error RawFit — fetch (if allowed) and read
// the model, then plan memory without starting anything.
func Ai_localFit(specArg any) any {
	return func() any {
		spec, err := aiResolveDirect(specArg)
		if err != nil {
			return aiTaskErr(err)
		}
		ctx, cancel := context.WithTimeout(context.Background(), aiStartTimeout())
		defer cancel()
		path, err := aiEnsureModel(ctx, spec.model)
		if err != nil {
			return aiTaskErr(err)
		}
		f, err := aiFitFor(path, spec, aiFitVariant())
		if err != nil {
			return aiTaskErr(err)
		}
		return Ok[any, any](aiFitValue(f))
	}
}

// Ai_localStart : RawSpec -> Task Error Int
func Ai_localStart(specArg any) any {
	return func() any {
		spec, err := aiResolveDirect(specArg)
		if err != nil {
			return aiTaskErr(err)
		}
		e := aiEngineFor("", spec)
		if testModeActive() {
			return Ok[any, any](int(aiEngineID(e)))
		}
		ctx, cancel := context.WithTimeout(context.Background(), aiStartTimeout())
		defer cancel()
		if err := e.ensure(ctx); err != nil {
			return aiTaskErr(err)
		}
		return Ok[any, any](int(aiEngineID(e)))
	}
}

// Ai_localPost : Int -> String -> String -> Task Error String
func Ai_localPost(idArg, pathArg, bodyArg any) any {
	id := int64(AsInt(idArg))
	path, body := AsString(pathArg), AsString(bodyArg)
	return func() any {
		e, err := aiEngineByID(id)
		if err != nil {
			return aiTaskErr(err)
		}
		body := aiPatchModel(body, "local")
		ctx, cancel := context.WithTimeout(context.Background(), aiLocalCallTimeout)
		defer cancel()
		if testModeActive() {
			c := &aiSlotConfig{slot: "local", timeout: aiLocalCallTimeout}
			out, err := aiRemotePost(ctx, c, aiTestLocalBase("local")+path, body)
			if err != nil {
				return aiTaskErr(err)
			}
			return Ok[any, any](out)
		}
		startCtx, cancelStart := context.WithTimeout(context.Background(), aiStartTimeout())
		err = e.ensure(startCtx)
		cancelStart()
		if err != nil {
			return aiTaskErr(err)
		}
		start := time.Now()
		out, err := e.post(ctx, aiLocalCallTimeout, path, body)
		rec := aiCallRecord{TS: start.UTC(), Slot: orDefault(e.slot, "local"), Mode: "local", Kind: aiCallKind(path),
			Model: e.spec.model.String(), Outcome: aiCallOutcome(err), LatencyMs: aiCallLatency(start)}
		if err == nil {
			rec.PromptTokens, rec.CompletionTokens = aiUsage(out)
		}
		aiRecordCall(rec)
		if err != nil {
			return aiTaskErr(err)
		}
		return Ok[any, any](out)
	}
}

// Ai_localStatus : Int -> Task Error Status
func Ai_localStatus(idArg any) any {
	id := int64(AsInt(idArg))
	return func() any {
		e, err := aiEngineByID(id)
		if err != nil {
			return aiTaskErr(err)
		}
		return Ok[any, any](e.status())
	}
}

// Ai_localStop : Int -> Task Error ()
func Ai_localStop(idArg any) any {
	id := int64(AsInt(idArg))
	return func() any {
		e, err := aiEngineByID(id)
		if err != nil {
			return aiTaskErr(err)
		}
		e.stop()
		return Ok[any, any](struct{}{})
	}
}

// Ai_localFetch : String -> Task Error String — make a pinned model present
// and verified (`sky ai provision` of a model), returning its path.
func Ai_localFetch(refArg any) any {
	ref := AsString(refArg)
	return func() any {
		m, err := aiParseModelRef(ref)
		if err != nil {
			return aiTaskErr(err)
		}
		ctx, cancel := context.WithTimeout(context.Background(), aiStartTimeout())
		defer cancel()
		p, err := aiEnsureModel(ctx, m)
		if err != nil {
			return aiTaskErr(err)
		}
		return Ok[any, any](p)
	}
}

// Ai_localPull : String -> String -> Task Error Pulled — fetch a Hugging Face
// file whose digest is not known yet, store it by its digest, and report the
// digest so it can be pinned in code (`sky ai pull`).
func Ai_localPull(repoArg, fileArg any) any {
	repo, file := strings.TrimSpace(AsString(repoArg)), strings.TrimSpace(AsString(fileArg))
	return func() any {
		if strings.Count(repo, "/") != 1 || file == "" {
			return aiTaskErr(aiFailf(aiInvalidSetting, "pull needs <org>/<repo> and a file name, got %q %q", repo, file))
		}
		ctx, cancel := context.WithTimeout(context.Background(), aiStartTimeout())
		defer cancel()
		path, sum, size, err := aiPullUnpinned(ctx, repo, file)
		if err != nil {
			return aiTaskErr(err)
		}
		return Ok[any, any](map[string]any{
			"repo": repo, "file": file, "path": path, "sha256": sum, "bytes": int(size),
		})
	}
}

// Ai_localProvision : () -> Task Error Provisioned — install the pinned
// engine for this host now (fetching is always allowed for an explicit
// provision).
func Ai_localProvision(_ any) any {
	return func() any {
		ctx, cancel := context.WithTimeout(context.Background(), aiStartTimeout())
		defer cancel()
		bin, err := aiProvisionEngine(ctx)
		if err != nil {
			return aiTaskErr(err)
		}
		return Ok[any, any](map[string]any{
			"path": bin.path, "build": bin.build, "variant": bin.variant,
			"platform": runtime.GOOS + "/" + runtime.GOARCH, "home": aiHome(),
		})
	}
}

// Ai_localEngines : () -> Task Error (List EngineInfo)
func Ai_localEngines(_ any) any {
	return func() any {
		out := aiRegistryList()
		if out == nil {
			out = []any{}
		}
		return Ok[any, any](out)
	}
}

// Ai_localStopSlot : String -> Task Error Int — stop the recorded engines of
// a slot ("" for all) and return how many were stopped.
func Ai_localStopSlot(slotArg any) any {
	slot := AsString(slotArg)
	return func() any {
		return Ok[any, any](aiStopSlot(slot))
	}
}
