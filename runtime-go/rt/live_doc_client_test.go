//go:build !js

package rt

import (
	"os"
	"os/exec"
	"strings"
	"testing"
)

// The client half of the "doc" operation (live_doc_client.go): it sets the
// new set, removes only the names it set before, never touches a runtime
// data-sky-* marker, refuses a name outside the allowed set, and reads the
// set from a full-body reply's trailing comment.
func TestLiveJS_AppliesTheDocOperation(t *testing.T) {
	node := requireNode(t)
	marker := appendLiveDocMetadata("<p>v</p>", true, []docAttr{{"lang", "ar"}, {"dir", "rtl"}})
	harness := `
const vm = require("vm");
const fs = require("fs");
const src = fs.readFileSync(process.argv[2], "utf8");
const attrs = { "data-sky-hydrating": "1", "lang": "en", "class": "x" };
const el = {
  getAttribute: (n) => (n in attrs ? attrs[n] : null),
  setAttribute: (n, v) => { attrs[n] = String(v); },
  removeAttribute: (n) => { delete attrs[n]; },
};
const errors = [];
const sandbox = { document: { documentElement: el }, console: { error: (m) => errors.push(m) },
  atob: (s) => Buffer.from(s, "base64").toString("binary"), TextDecoder, Uint8Array, JSON, Array, Object };
sandbox.window = sandbox;
vm.createContext(sandbox);
vm.runInContext(src, sandbox);
// The page's own set: the server wrote lang and class.
sandbox.__skyApplyDoc([{ name: "lang", value: "en" }, { name: "class", value: "x" }]);
sandbox.__skyApplyDoc([{ name: "lang", value: "ar" }, { name: "dir", value: "rtl" },
  { name: "data-sky-hydrating", value: "0" }, { name: "onclick", value: "x" }]);
const reply = sandbox.__skyDocBodyReply(process.argv[3]);
process.stdout.write(JSON.stringify({ attrs, errors: errors.length, body: reply.body, doc: reply.doc }));
`
	dir := t.TempDir()
	jsPath := dir + "/doc.js"
	harnessPath := dir + "/harness.js"
	if err := os.WriteFile(jsPath, []byte(liveDocClientJS), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(harnessPath, []byte(harness), 0o600); err != nil {
		t.Fatal(err)
	}
	out, err := exec.Command(node, harnessPath, jsPath, marker).CombinedOutput()
	if err != nil {
		t.Fatalf("node harness failed: %v\n%s", err, out)
	}
	got := string(out)
	for _, want := range []string{
		`"attrs":{"data-sky-hydrating":"1","lang":"ar","dir":"rtl"}`,
		`"errors":2`,
		`"body":"<p>v</p>"`,
		`"doc":[{"name":"lang","value":"ar"},{"name":"dir","value":"rtl"}]`,
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("missing %s in %s", want, got)
		}
	}
}
