//go:build !js

package rt

// The Std.Ai.Device browser worker (ai_device_worker.go), run under node with
// a fake engine module, a fake origin-private file system and a fake fetch:
//
//   - a WebGPU engine that registers no GPU device fails with
//     DeviceUnsupported, and the CPU engine is never loaded in its place
//     (the `ai-device-no-silent-fallback` gate);
//   - the streaming SHA-256 matches the platform's, a chunk that does not
//     match its digest is ChecksumMismatch, and a good download is stored
//     and marked ready (the `ai-device-digest` gate);
//   - a store the browser refuses is StorageDenied.

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// workerHarness is the node side: it gives the worker script the browser
// globals it uses, then runs one scenario and prints a JSON line per check.
const workerHarness = `
const scenario = process.argv[2];
const dir = process.argv[3];
const files = new Map();      // OPFS: name -> Uint8Array
const loaded = [];            // engine variants the worker imported
let storageRefused = scenario === 'refused-store';
function handle(name) {
  return {
    async getFile() { const b = files.get(name) || new Uint8Array(0); return new Blob([b]); },
    async createSyncAccessHandle() {
      return { write(bytes, opt) { const at = (opt && opt.at) || 0; const cur = files.get(name) || new Uint8Array(0);
                 const next = new Uint8Array(Math.max(cur.length, at + bytes.length)); next.set(cur); next.set(bytes, at); files.set(name, next); },
               flush() {}, close() {} };
    },
  };
}
const store = {
  async getFileHandle(name, opt) {
    if (!files.has(name)) { if (!(opt && opt.create)) throw new Error('NotFoundError'); files.set(name, new Uint8Array(0)); }
    return handle(name);
  },
  async removeEntry(name) { files.delete(name); },
  async *entries() { for (const k of files.keys()) yield [k, handle(k)]; },
};
const posted = [];
globalThis.self = globalThis;
globalThis.postMessage = (m) => posted.push(m);
Object.defineProperty(globalThis, 'navigator', { value: {
  storage: {
    async getDirectory() { if (storageRefused) throw new Error('The operation failed for an unknown transient reason'); return { async getDirectoryHandle() { return store; } }; },
    async estimate() { return { quota: 1e10, usage: 0 }; },
  },
  gpu: { async requestAdapter() { return { features: new Set(['shader-f16']), limits: {} }; } },
}, configurable: true });
globalThis.WebAssembly.Suspending = function () {};
const model = new Uint8Array(150).map((_, i) => (i * 7) & 255);
const CHUNK = 64 * 1024 * 1024;
const fake = { chunks: null };
globalThis.fetch = async (url, init) => {
  if (url.endsWith('.chunks.json')) return { ok: true, status: 200, json: async () => fake.chunks };
  return { ok: true, status: 206, arrayBuffer: async () => fake.body.buffer.slice(0) };
};
// Fake engines: the WebGPU one registers the number of GPUs the scenario asks for.
const engine = (variant, gpus) => 'data:text/javascript,' + encodeURIComponent(
  'export default async function create() { globalThis.__loaded.push(' + JSON.stringify(variant) + ');' +
  ' return { FS: { mkdir() {}, mount() {}, unmount() {}, filesystems: { WORKERFS: {} } },' +
  ' ccall(name) { if (name === "sky_llama_gpu_devices") return ' + gpus + '; if (name === "sky_llama_last_error") return ""; if (name === "sky_llama_chat") return JSON.stringify({choices:[{message:{content:"on " + ' + JSON.stringify(variant) + '}}]}); return 0; } }; }');
globalThis.__loaded = loaded;
await import(dir + '/worker.mjs');
const ask = (msg) => new Promise((res) => { const n = posted.length; self.onmessage({ data: msg });
  const t = setInterval(() => { const r = posted.slice(n).find((m) => m.id === msg.id); if (r) { clearInterval(t); res(r); } }, 5); });
const sha = async (u8) => Array.from(new Uint8Array(await crypto.subtle.digest('SHA-256', u8)), (b) => b.toString(16).padStart(2, '0')).join('');
const out = (o) => console.log(JSON.stringify(o));
const modelSha = await sha(model);
const offer = (sum, bytes) => JSON.stringify({ slot: 'chat', placement: 'auto', model: { label: 'm.gguf', sha256: sum, bytes, url: '/_sky/ai/blob/' + sum }, context: 2048, maxDownloadBytes: 1e9, backends: ['webgpu', 'wasm-cpu'], metered: false, server: true, here: false });
if (scenario === 'no-gpu' || scenario === 'gpu') {
  files.set(modelSha + '.gguf', model); files.set(modelSha + '.ok', new Uint8Array(0));
  const engines = { webgpu: { mjs: engine('webgpu', scenario === 'gpu' ? 1 : 0), wasm: '' }, cpu: { mjs: engine('cpu', 0), wasm: '' } };
  const r = await ask({ id: 1, op: 'chat', backend: 'webgpu', slot: 'chat', offer: offer(modelSha, model.length), path: '/chat/completions', body: '{"messages":[]}', engines });
  out({ ok: r.ok, error: r.error || '', value: r.value || '', loaded });
} else if (scenario === 'download' || scenario === 'corrupt') {
  fake.body = model; fake.chunks = { sha256: modelSha, size: model.length, chunkBytes: CHUNK, chunks: [scenario === 'corrupt' ? '00'.repeat(32) : modelSha] };
  const r = await ask({ id: 2, op: 'prepare', backend: 'wasm-cpu', slot: 'chat', key: 'k', offer: offer(modelSha, model.length), engines: { cpu: { mjs: engine('cpu', 0), wasm: '' } } });
  out({ ok: r.ok, error: r.error || '', stored: files.has(modelSha + '.ok'), bytesEqual: files.has(modelSha + '.gguf') && (await sha(files.get(modelSha + '.gguf'))) === modelSha });
} else if (scenario === 'wrong-whole') {
  // Every chunk matches its listed digest, but the list claims a whole-file digest the bytes do not have.
  const claimed = 'ab'.repeat(32);
  fake.body = model; fake.chunks = { sha256: claimed, size: model.length, chunkBytes: CHUNK, chunks: [modelSha] };
  const r = await ask({ id: 3, op: 'prepare', backend: 'wasm-cpu', slot: 'chat', key: 'k', offer: offer(claimed, model.length), engines: { cpu: { mjs: engine('cpu', 0), wasm: '' } } });
  out({ ok: r.ok, error: r.error || '', stored: files.has(claimed + '.ok') });
} else if (scenario === 'sha') {
  // The worker's streaming SHA-256 against the platform's, over odd chunk sizes.
  const src = await (await import('node:fs/promises')).readFile(new URL(dir + '/worker.mjs'), 'utf8');
  const ShaCtor = new Function(src.slice(src.indexOf('const K = '), src.indexOf('async function digestHex')) + '; return Sha256;')();
  const results = [];
  for (const n of [0, 1, 55, 56, 63, 64, 65, 1000, 70000]) {
    const data = new Uint8Array(n).map((_, i) => (i * 31 + 7) & 255);
    const h = new ShaCtor();
    for (let i = 0; i < n; i += 37) h.update(data.subarray(i, Math.min(n, i + 37)));
    results.push(h.hex() === (await sha(data)));
  }
  out({ all: results.every(Boolean), results });
} else if (scenario === 'refused-store') {
  const r = await ask({ id: 4, op: 'caps', engines: { cpu: { mjs: 'x', wasm: '' } } });
  const p = await ask({ id: 5, op: 'prepare', backend: 'wasm-cpu', slot: 'chat', key: 'k', offer: offer(modelSha, model.length), engines: {} });
  out({ caps: JSON.parse(r.value), prepareError: p.error || '' });
}
`

// runWorker runs one harness scenario and decodes its JSON line.
func runWorker(t *testing.T, scenario string) map[string]any {
	t.Helper()
	node := requireNode(t)
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "worker.mjs"), []byte(AiDeviceWorkerJS), 0o644); err != nil {
		t.Fatal(err)
	}
	h := filepath.Join(dir, "harness.mjs")
	if err := os.WriteFile(h, []byte(workerHarness), 0o644); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(node, h, scenario, "file://"+dir)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("%s: node: %v\n%s", scenario, err, out)
	}
	lines := strings.Split(strings.TrimSpace(string(out)), "\n")
	var v map[string]any
	if err := json.Unmarshal([]byte(lines[len(lines)-1]), &v); err != nil {
		t.Fatalf("%s: %v\n%s", scenario, err, out)
	}
	return v
}

func wireCode(t *testing.T, s string) string {
	t.Helper()
	f, ok := aiDeviceFailureFrom([]byte(strings.TrimPrefix(s, "ai:")))
	if !strings.HasPrefix(s, "ai:") || !ok {
		return ""
	}
	return string(f.code)
}

// TestDeviceWebGpuWithoutAGpuIsDeviceUnsupported: a WebGPU engine that
// registers no GPU device never answers on the CPU in silence.
func TestDeviceWebGpuWithoutAGpuIsDeviceUnsupported(t *testing.T) {
	v := runWorker(t, "no-gpu")
	if v["ok"] == true || wireCode(t, v["error"].(string)) != "DeviceUnsupported" {
		t.Fatalf("no GPU device: %v", v)
	}
	if !strings.Contains(v["error"].(string), "webgpu") {
		t.Fatalf("the failure names the backend: %v", v)
	}
	loaded := v["loaded"].([]any)
	if len(loaded) != 1 || loaded[0] != "webgpu" {
		t.Fatalf("only the WebGPU engine was loaded, never the CPU one in its place: %v", loaded)
	}
	// The twin: with a GPU device the same request answers on WebGPU.
	v = runWorker(t, "gpu")
	if v["ok"] != true || !strings.Contains(v["value"].(string), "on webgpu") {
		t.Fatalf("with a GPU: %v", v)
	}
}

// TestDeviceWorkerDigests: the streaming SHA-256, the per-chunk check, the
// whole-file check, and a verified download marked ready.
func TestDeviceWorkerDigests(t *testing.T) {
	v := runWorker(t, "sha")
	if v["all"] != true {
		t.Fatalf("the worker's SHA-256 differs from the platform's: %v", v)
	}
	v = runWorker(t, "download")
	if v["ok"] != true || v["stored"] != true || v["bytesEqual"] != true {
		t.Fatalf("a verified download: %v", v)
	}
	v = runWorker(t, "corrupt")
	if v["ok"] == true || wireCode(t, v["error"].(string)) != "ChecksumMismatch" || v["stored"] == true {
		t.Fatalf("a chunk that does not match: %v", v)
	}
	v = runWorker(t, "wrong-whole")
	if v["ok"] == true || wireCode(t, v["error"].(string)) != "ChecksumMismatch" || v["stored"] == true {
		t.Fatalf("a file that does not match the offer's digest: %v", v)
	}
}

// TestDeviceWorkerRefusedStoreIsStorageDenied: a private window's store.
func TestDeviceWorkerRefusedStoreIsStorageDenied(t *testing.T) {
	v := runWorker(t, "refused-store")
	caps := v["caps"].(map[string]any)
	if caps["storageAvailableBytes"].(float64) != 0 {
		t.Fatalf("a refused store offers no room: %v", caps)
	}
	if wireCode(t, v["prepareError"].(string)) != "StorageDenied" {
		t.Fatalf("prepare on a refused store: %v", v)
	}
}

