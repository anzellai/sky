//go:build !js

package rt

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestAdapterMaxDiffMeasuresTheLargestDifference(t *testing.T) {
	dir := t.TempDir()
	write := func(name string, ts map[string]aiTensor) string {
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, aiSafetensorsBytes(ts, nil), 0o644); err != nil {
			t.Fatal(err)
		}
		return p
	}
	a := write("a.safetensors", map[string]aiTensor{
		"x": {shape: []int{2, 2}, data: []float32{1, 2, 3, 4}},
		"y": {shape: []int{1}, data: []float32{0.5}},
	})
	b := write("b.safetensors", map[string]aiTensor{
		"x": {shape: []int{2, 2}, data: []float32{1, 2, 3.25, 4}},
		"y": {shape: []int{1}, data: []float32{0.5}},
	})
	out, err := AiAdapterMaxDiff(a, b)
	if err != nil {
		t.Fatal(err)
	}
	if out["maxAbsDiff"].(float64) != 0.25 || out["tensors"].(int) != 2 {
		t.Fatalf("got %v", out)
	}
	if out, err := AiAdapterMaxDiff(a, a); err != nil || out["maxAbsDiff"].(float64) != 0 {
		t.Fatalf("an adapter against itself: %v %v", out, err)
	}
	c := write("c.safetensors", map[string]aiTensor{
		"x": {shape: []int{4}, data: []float32{1, 2, 3, 4}},
		"y": {shape: []int{1}, data: []float32{0.5}},
	})
	if _, err := AiAdapterMaxDiff(a, c); err == nil || !strings.Contains(err.Error(), "tensor x") {
		t.Fatalf("a shape mismatch must fail naming the tensor, got %v", err)
	}
}
