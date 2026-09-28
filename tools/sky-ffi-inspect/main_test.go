package main

import (
	"encoding/json"
	"strings"
	"testing"
)

// A package Go cannot load has no functions. Its report must still write every
// list as `[]`: the consumer rejects `null` and would hide the real cause.
func TestReportNeverWritesNullLists(t *testing.T) {
	cases := []PackageInfo{
		{Pkg: "example.local/hello", Errors: []string{"load: go: updates to go.mod needed"}},
		{Pkg: "example.local/hello", Name: "hello"},
		{Pkg: "p", Functions: []Function{{Name: "F"}}, Implements: map[string][]string{"T@p": nil}},
	}
	for _, c := range cases {
		b, err := json.Marshal(withEmptyLists(c))
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(b), "null") {
			t.Errorf("report carries a null list: %s", b)
		}
	}
	b, _ := json.Marshal(withEmptyLists(cases[0]))
	if !strings.Contains(string(b), `"functions":[]`) || !strings.Contains(string(b), "updates to go.mod needed") {
		t.Errorf("unexpected report: %s", b)
	}
}
