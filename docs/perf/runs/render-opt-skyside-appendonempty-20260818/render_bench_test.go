package main

import "testing"

// Measures the Std.Ui Element->Html pass (Main_renderForum ->
// Std_Ui_layout -> renderElement/renderNodeAs -> Std_Html_render) at the
// render re-baseline's view sizes: ~16 Std.Ui elements/post, so 6 posts
// ~= 94 sky-id elements and 60 posts ~= 974. Allocation is the grounded
// metric (reproduces tightly); CPU is reported with spread.
var forumCounts = []struct {
	label  string
	nPosts int
}{
	{"posts=6_~94el", 6},
	{"posts=60_~974el", 60},
}

var sink int

func BenchmarkRenderForum(b *testing.B) {
	for _, c := range forumCounts {
		b.Run(c.label, func(b *testing.B) {
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				sink += len(Main_renderForum(c.nPosts))
			}
		})
	}
}
