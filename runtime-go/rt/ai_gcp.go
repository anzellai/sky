//go:build !js

package rt

// ai_gcp.go — the runtime half of Std.Ai.Cloud.Google: the emulator seam.
//
// Std.Ai.Cloud.Google is Sky over Sky.Core.Http, so this file holds no
// Google client. It answers one question, where the calls go:
//
//	Ai_gcpEndpoint : () -> Task Error String
//	Ai_tarGz       : String -> String -> Task Error Blob   a Cloud Build source
//
// `<PREFIX>_AI_GCP_ENDPOINT` sends every Google call (OAuth, metadata,
// Vertex AI, Cloud Run, GCS, Artifact Registry, Cloud Quotas, Logging) to one
// server, under fixed path prefixes. Under test mode the host
// `sky-gcp.invalid` is answered by the built-in emulator (ai_gcpfake.go), so
// `SKY_AI_GCP_ENDPOINT=http://sky-gcp.invalid` in a project's `.env.test`
// runs a whole Vertex training run offline. Outside test mode the variable
// names a real server (the `rt/testdata/fakegcp` binary in the cargo flows).

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"io"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
)

// aiGcpFakeHost is the host the test-mode transport answers with the
// built-in emulator.
const aiGcpFakeHost = "sky-gcp.invalid"

// aiGcpEndpoint is the emulator's base URL, or "" for Google itself.
func aiGcpEndpoint() string {
	for _, v := range configLayers("AI_GCP_ENDPOINT", "") {
		if v = strings.TrimSpace(v); v != "" {
			return strings.TrimRight(v, "/")
		}
	}
	return ""
}

// Ai_gcpEndpoint : () -> Task Error String
func Ai_gcpEndpoint(_ any) any {
	return func() any { return Ok[any, any](aiGcpEndpoint()) }
}

var (
	aiGcpFakeOnce   sync.Once
	aiGcpFakeShared *aiFakeGCP
)

// aiGcpFakeInProcess is the test-mode emulator, one per process.
func aiGcpFakeInProcess() *aiFakeGCP {
	aiGcpFakeOnce.Do(func() { aiGcpFakeShared = newAiFakeGCP() })
	return aiGcpFakeShared
}

// aiGcpFakeRoundTrip answers a test-mode request to sky-gcp.invalid with the
// in-process emulator. body is the request body the transport already read.
func aiGcpFakeRoundTrip(req *http.Request, body string) *http.Response {
	r := req.Clone(req.Context())
	r.Body = io.NopCloser(strings.NewReader(body))
	r.ContentLength = int64(len(body))
	rec := httptest.NewRecorder()
	aiGcpFakeInProcess().ServeHTTP(rec, r)
	res := rec.Result()
	data, _ := io.ReadAll(res.Body)
	res.Body = io.NopCloser(bytes.NewReader(data))
	res.Request = req
	return res
}

// Ai_tarGz : String -> String -> Task Error Blob — pack a directory into a
// gzipped tar at `out` (a Cloud Build source), reproducibly: entries sorted,
// modes kept, times and owners zeroed, so the same tree is the same digest.
// Hidden files and directories are left out.
func Ai_tarGz(dirArg, outArg any) any {
	dir, out := AsString(dirArg), AsString(outArg)
	return func() any {
		if err := aiTarGz(dir, out); err != nil {
			return aiTaskErr(aiFailf(aiInvalidSetting, "cannot pack %s: %v", dir, err).with("setting", "context"))
		}
		sum, size, err := aiHashFile(out)
		if err != nil {
			return aiTaskErr(aiFailf(aiInvalidSetting, "cannot read %s: %v", out, err).with("setting", "context"))
		}
		return Ok[any, any](aiDigestValue(sum, size))
	}
}

func aiTarGz(dir, out string) error {
	var files []string
	err := filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if p != dir && strings.HasPrefix(d.Name(), ".") {
			if d.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if d.Type().IsRegular() {
			files = append(files, p)
		}
		return nil
	})
	if err != nil {
		return err
	}
	sort.Strings(files)
	f, err := os.Create(out + ".partial")
	if err != nil {
		return err
	}
	gz := gzip.NewWriter(f)
	tw := tar.NewWriter(gz)
	for _, p := range files {
		st, err := os.Stat(p)
		if err != nil {
			f.Close()
			return err
		}
		rel, _ := filepath.Rel(dir, p)
		hdr := &tar.Header{Name: filepath.ToSlash(rel), Mode: int64(st.Mode().Perm()), Size: st.Size(), Typeflag: tar.TypeReg, Format: tar.FormatPAX}
		if err := tw.WriteHeader(hdr); err != nil {
			f.Close()
			return err
		}
		src, err := os.Open(p)
		if err != nil {
			f.Close()
			return err
		}
		_, err = io.Copy(tw, src)
		src.Close()
		if err != nil {
			f.Close()
			return err
		}
	}
	if err := tw.Close(); err != nil {
		f.Close()
		return err
	}
	if err := gz.Close(); err != nil {
		f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	return os.Rename(out+".partial", out)
}
