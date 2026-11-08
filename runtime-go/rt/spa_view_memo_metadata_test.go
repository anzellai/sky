package rt

import "testing"

func spaViewMemoRestoreMetadata(t *testing.T) {
	old := codecEnumRegistry()
	t.Cleanup(func() { enumRegistryWriter.Lock(); enumRegistry.Store(old); enumRegistryWriter.Unlock() })
}
func spaViewMemoMetadataCall(c *spaViewMemoCache, builds *int, name string) any {
	frame := c.begin(nil)
	result := SpaViewMemoRegion(frame, 0, []any{SpaViewMemoCapture(1)}, func() any { *builds++; return EnumName(name, 0) })
	frame.seal()
	frame.finish(true)
	return result
}

// Original API RED: no new enum factory/shape is needed to expose stale reuse.
func TestSpaViewMemoMetadataChangeBeforeBeginInvalidatesPreviousResult(t *testing.T) {
	spaViewMemoRestoreMetadata(t)
	const name = "piv_ViewMemo_MetadataBefore"
	RegisterEnum(name, []string{"before"})
	cache := &spaViewMemoCache{}
	builds := 0
	if spaViewMemoMetadataCall(cache, &builds, name) != "before" {
		t.Fatal("first value")
	}
	RegisterEnum(name, []string{"after"})
	if spaViewMemoMetadataCall(cache, &builds, name) != "after" || builds != 2 {
		t.Fatal("metadata change reused stale pure result")
	}
}

func TestSpaViewMemoMetadataChangeDuringFrameAndBuildDoesNotReplayOrPublish(t *testing.T) {
	spaViewMemoRestoreMetadata(t)
	const name = "piv_ViewMemo_MetadataDuring"
	RegisterEnum(name, []string{"before"})
	cache := &spaViewMemoCache{}
	builds := 0
	spaViewMemoMetadataCall(cache, &builds, name)
	frame := cache.begin(nil)
	RegisterEnum(name, []string{"after-begin"})
	result := SpaViewMemoRegion(frame, 0, []any{SpaViewMemoCapture(1)}, func() any { builds++; return EnumName(name, 0) })
	frame.seal()
	frame.finish(true)
	if result != "after-begin" || builds != 2 || len(cache.current) != 0 {
		t.Fatal("stale frame hit/published after metadata mutation")
	}
	frame = cache.begin(nil)
	result = SpaViewMemoRegion(frame, 0, []any{SpaViewMemoCapture(2)}, func() any {
		builds++
		RegisterEnum(name, []string{"during-build"})
		return EnumName(name, 0)
	})
	frame.seal()
	frame.finish(true)
	if result != "during-build" || builds != 3 || len(cache.current) != 0 {
		t.Fatal("metadata-changing build replayed or was certified")
	}
}

func TestSpaViewMemoOwnerEpochChangeBeforeFinishDiscardsStagedHits(t *testing.T) {
	spaViewMemoRestoreMetadata(t)
	const name = "piv_ViewMemo_MetadataFinish"
	RegisterEnum(name, []string{"value"})
	cache := &spaViewMemoCache{}
	builds := 0
	spaViewMemoMetadataCall(cache, &builds, name)
	frame := cache.begin(nil)
	SpaViewMemoRegion(frame, 0, []any{SpaViewMemoCapture(1)}, func() any { t.Fatal("expected pre-mutation hit"); return nil })
	registerCodecDeclaredOwner("piv_ViewMemo_MetadataOwner", "sky:Piv.MetadataOwner")
	frame.seal()
	frame.finish(true)
	if len(cache.current) != 0 {
		t.Fatal("owner epoch change published stale staged hit")
	}
	spaViewMemoMetadataCall(cache, &builds, name)
	if builds != 2 {
		t.Fatal("post-owner-change frame did not rebuild")
	}
}
