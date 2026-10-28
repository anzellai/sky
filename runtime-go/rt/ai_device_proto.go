package rt

// ai_device_proto.go — the wire values of Std.Ai.Device, shared by the
// server (ai_device_serve.go), the Sky.Spa client in the browser
// (ai_device_wasm.go), the desktop shell (ai_device_desktop.go) and the
// native shells (the Swift and Java templates in rust/crates/sky/src/main.rs
// speak the same JSON).
//
//   - The offer: GET /_sky/ai/device/<slot>, what the server lets a device
//     download and run for one slot (Std.Ai.Device.Offer).
//   - The capabilities: what a device can do (Std.Ai.Device.Capabilities).
//   - The progress of a download (`state` is idle, downloading, ready or
//     failed).
//   - A failure that crosses the wire: its Std.Ai.Failure code, attributes
//     and message, so the client rebuilds the same typed `Error`.
//
// Every value is JSON in both directions and the Sky side decodes it, so no
// record crosses the kernel boundary.

import (
	"encoding/json"
	"errors"
	"strconv"
)

// The device backends, as Std.Ai.Device.backendText writes them.
const (
	aiDeviceNative  = "native"
	aiDeviceWebGpu  = "webgpu"
	aiDeviceWasmCpu = "wasm-cpu"
)

// aiDeviceChunkBytes is the size of one download chunk: the unit a device
// fetches with one Range request, verifies and keeps, so an interrupted
// download resumes from the last whole chunk.
const aiDeviceChunkBytes = 64 << 20

// aiDeviceDefaultMaxDownload is the largest download a slot offers when its
// spec names none (1 GiB): above it a browser tab is likely to be evicted.
const aiDeviceDefaultMaxDownload = 1 << 30

// aiDeviceDefaultContext is a device's context window when the spec names
// none.
const aiDeviceDefaultContext = 2048

// aiDeviceFile is one file an offer lets a device download.
type aiDeviceFile struct {
	Label  string `json:"label"`
	Sha256 string `json:"sha256"`
	Bytes  int64  `json:"bytes"`
	URL    string `json:"url"`
}

// aiDeviceOffer is the server's offer for one slot.
type aiDeviceOffer struct {
	Slot             string        `json:"slot"`
	Placement        string        `json:"placement"`
	Model            aiDeviceFile  `json:"model"`
	Adapter          *aiDeviceFile `json:"adapter,omitempty"`
	Context          int           `json:"context"`
	Threads          int           `json:"threads"`   // 0: the engine's own
	GpuLayers        int           `json:"gpuLayers"` // -1: the engine's own
	MaxDownloadBytes int64         `json:"maxDownloadBytes"`
	Backends         []string      `json:"backends"`
	Metered          bool          `json:"metered"`
	Server           bool          `json:"server"`
	Here             bool          `json:"here"`
	Refused          string        `json:"refused,omitempty"`
}

// aiDeviceBackendInfo is one backend a device can run.
type aiDeviceBackendInfo struct {
	Backend       string `json:"backend"`
	MaxModelBytes int64  `json:"maxModelBytes"`
	// Gpu: the backend can offload layers to a GPU. A slot that asks for GPU
	// layers (Device.withGpuLayers above 0) does not use a backend without.
	Gpu bool `json:"gpu"`
}

// aiDeviceUnsupportedBackend is a backend a device cannot use, and why.
type aiDeviceUnsupportedBackend struct {
	Backend string `json:"backend"`
	Reason  string `json:"reason"`
}

// aiDeviceCaps is what a device can do.
type aiDeviceCaps struct {
	Platform              string                       `json:"platform"`
	Backends              []aiDeviceBackendInfo        `json:"backends"`
	Unsupported           []aiDeviceUnsupportedBackend `json:"unsupported"`
	StorageAvailableBytes int64                        `json:"storageAvailableBytes"`
	Metered               bool                         `json:"metered"`
	Ready                 []string                     `json:"ready"`
	Failed                []string                     `json:"failed"`
}

// normalise gives every list a value, so the JSON has `[]`, never `null`
// (the Sky decoder reads a list).
func (c *aiDeviceCaps) normalise() {
	if c.Backends == nil {
		c.Backends = []aiDeviceBackendInfo{}
	}
	if c.Unsupported == nil {
		c.Unsupported = []aiDeviceUnsupportedBackend{}
	}
	if c.Ready == nil {
		c.Ready = []string{}
	}
	if c.Failed == nil {
		c.Failed = []string{}
	}
}

// aiDeviceProgress is the state of a slot's download on one device.
type aiDeviceProgress struct {
	State      string `json:"state"`
	DoneBytes  int64  `json:"doneBytes"`
	TotalBytes int64  `json:"totalBytes"`
	Error      string `json:"error,omitempty"`
}

// aiDeviceWireFailure is a typed AI failure as JSON: the server's answer to
// a failed offer or fallback call, and a native shell's or the worker's
// rejection. Code is a Std.Ai.Failure constructor name.
type aiDeviceWireFailure struct {
	Code    string      `json:"code"`
	Message string      `json:"message"`
	Attrs   [][2]string `json:"attrs"`
}

// aiDeviceFailureJSON is the wire form of an error: a classified AI failure
// keeps its code and attributes; any other error is an EngineFailed.
func aiDeviceFailureJSON(err error) []byte {
	w := aiDeviceWireFailure{Code: string(aiEngineFailed), Message: err.Error()}
	var f *aiFailure
	if errors.As(err, &f) {
		_, df := aiDomainFailure(f)
		w = aiDeviceWireFailure{Code: string(f.code), Message: f.msg, Attrs: df.Attrs}
	}
	if w.Attrs == nil {
		w.Attrs = [][2]string{}
	}
	b, _ := json.Marshal(map[string]any{"failure": w})
	return b
}

// aiDeviceFailureFrom reads a wire failure back as the same classified
// failure. ok is false when the body is not a wire failure.
func aiDeviceFailureFrom(body []byte) (*aiFailure, bool) {
	var env struct {
		Failure *aiDeviceWireFailure `json:"failure"`
	}
	if json.Unmarshal(body, &env) != nil || env.Failure == nil || env.Failure.Code == "" {
		return nil, false
	}
	w := env.Failure
	f := &aiFailure{code: aiFailCode(w.Code), msg: w.Message}
	for _, kv := range w.Attrs {
		f.attrs = append(f.attrs, kv)
	}
	if w.Code == string(aiRemoteRejected) {
		for _, kv := range w.Attrs {
			if kv[0] == "status" {
				f.httpStatus, _ = strconv.Atoi(kv[1])
			}
		}
	}
	return f, true
}

// aiDeviceShellFailure reads a native shell's or the worker's rejection
// text. A shell rejects with `ai:<json wire failure>` for a typed failure,
// or with the `<kind>: <message>` text every native op uses.
func aiDeviceShellFailure(text string) error {
	const prefix = "ai:"
	if len(text) > len(prefix) && text[:len(prefix)] == prefix {
		if f, ok := aiDeviceFailureFrom([]byte(text[len(prefix):])); ok {
			return f
		}
	}
	return aiFailf(aiEngineFailed, "%s", text)
}

// aiDeviceUnsupportedf is the DeviceUnsupported failure for a backend.
func aiDeviceUnsupportedf(backend, format string, args ...any) *aiFailure {
	f := aiFailf(aiDeviceUnsupported, format, args...)
	return f.with("backend", backend).with("reason", f.msg)
}

// aiDeviceValidBackend reports whether a text names a device backend.
func aiDeviceValidBackend(b string) bool {
	switch b {
	case aiDeviceNative, aiDeviceWebGpu, aiDeviceWasmCpu:
		return true
	}
	return false
}
