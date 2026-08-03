// remote.go — `sky bluedb --url <app> --token <tok>` live mode: inspect/edit a
// RUNNING app's store over its authenticated admin endpoint
// (/_sky/console/api/data), with zero downtime. The app is the sole writer; this
// asks it to perform the op. Config can come from a .env via --env (keeps the
// token out of argv/shell-history).
package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"
)

// resolveRemoteConfig fills url/token from --env and the process env, without
// overriding explicit flags. Precedence: flag > --env file > process env.
func resolveRemoteConfig(f *flags) {
	if f.envFile != "" {
		kv := loadEnvFile(f.envFile)
		if f.url == "" {
			f.url = kv["SKY_BLUEDB_URL"]
		}
		if f.token == "" {
			f.token = kv["SKY_ADMIN_TOKEN"]
		}
	}
	if f.url == "" {
		f.url = os.Getenv("SKY_BLUEDB_URL")
	}
	if f.token == "" {
		f.token = os.Getenv("SKY_ADMIN_TOKEN")
	}
}

func loadEnvFile(path string) map[string]string {
	out := map[string]string{}
	file, err := os.Open(path)
	if err != nil {
		return out
	}
	defer file.Close()
	sc := bufio.NewScanner(file)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		line = strings.TrimPrefix(line, "export ")
		k, v, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		k = strings.TrimSpace(k)
		v = strings.TrimSpace(v)
		v = strings.Trim(v, `"'`)
		out[k] = v
	}
	return out
}

func runRemote(positional []string, f flags, stdin io.Reader, stdout, stderr io.Writer) int {
	base := strings.TrimRight(f.url, "/") + "/_sky/console/api/data"
	rc := &remoteClient{base: base, token: f.token, http: &http.Client{Timeout: 20 * time.Second}}

	// `stores` (or no positional) → list the open app stores.
	if len(positional) == 0 || positional[0] == "stores" {
		return rc.listStores(f, stdout, stderr)
	}
	store := positional[0]
	if len(positional) < 2 {
		fmt.Fprintln(stderr, "bluedb: remote mode needs a command: <store> <scan|keys|get|put|delete>")
		return 2
	}
	cmd := positional[1]
	rest := positional[2:]

	switch cmd {
	case "scan", "keys":
		prefix := ""
		if len(rest) > 0 {
			prefix = rest[0]
		}
		return rc.scan(store, prefix, cmd == "keys", f, stdout, stderr)
	case "get":
		if len(rest) < 1 {
			fmt.Fprintln(stderr, "bluedb: get needs a <key>")
			return 2
		}
		return rc.get(store, rest[0], f, stdout, stderr)
	case "put":
		if len(rest) < 2 {
			fmt.Fprintln(stderr, "bluedb: put needs <key> <value>")
			return 2
		}
		return rc.mutate(store, "put", rest[0], rest[1], f, stdin, stdout, stderr)
	case "delete", "del", "rm":
		if len(rest) < 1 {
			fmt.Fprintln(stderr, "bluedb: delete needs a <key>")
			return 2
		}
		return rc.mutate(store, "delete", rest[0], "", f, stdin, stdout, stderr)
	case "stats":
		fmt.Fprintln(stderr, "bluedb: `stats` is offline-only; use `scan` remotely, or `stores` to list stores")
		return 2
	default:
		fmt.Fprintf(stderr, "bluedb: unknown remote command %q\n", cmd)
		return 2
	}
}

type remoteClient struct {
	base  string
	token string
	http  *http.Client
}

func (rc *remoteClient) do(method, urlStr string, body []byte) (*http.Response, error) {
	var rdr io.Reader
	if body != nil {
		rdr = bytes.NewReader(body)
	}
	req, err := http.NewRequest(method, urlStr, rdr)
	if err != nil {
		return nil, err
	}
	if rc.token != "" {
		req.Header.Set("Authorization", "Bearer "+rc.token)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("X-Sky-Console", "1")
	}
	return rc.http.Do(req)
}

func (rc *remoteClient) fail(resp *http.Response, stderr io.Writer) int {
	b, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
	msg := strings.TrimSpace(string(b))
	switch resp.StatusCode {
	case http.StatusUnauthorized:
		fmt.Fprintln(stderr, "bluedb: unauthorized — set --token / SKY_ADMIN_TOKEN (the app's admin bearer)")
	case http.StatusNotFound:
		fmt.Fprintf(stderr, "bluedb: not found (%s) — is the data endpoint enabled (SKY_CONSOLE_DATA) and the store open?\n", msg)
	default:
		fmt.Fprintf(stderr, "bluedb: remote error %d: %s\n", resp.StatusCode, msg)
	}
	return 1
}

func (rc *remoteClient) listStores(f flags, stdout, stderr io.Writer) int {
	resp, err := rc.do("GET", rc.base, nil)
	if err != nil {
		fmt.Fprintf(stderr, "bluedb: connect %s: %v\n", rc.base, err)
		return 1
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return rc.fail(resp, stderr)
	}
	var out struct {
		Stores   []string `json:"stores"`
		Writable bool     `json:"writable"`
	}
	_ = json.NewDecoder(resp.Body).Decode(&out)
	if f.json {
		enc := json.NewEncoder(stdout)
		enc.SetIndent("", "  ")
		_ = enc.Encode(out)
		return 0
	}
	for _, s := range out.Stores {
		fmt.Fprintln(stdout, s)
	}
	if !out.Writable {
		fmt.Fprintln(stderr, "(read-only: set SKY_CONSOLE_DATA=readwrite on the app to enable edits)")
	}
	return 0
}

type remoteRow struct {
	Key   string `json:"key"`
	Value string `json:"value"`
}

func (rc *remoteClient) fetchRows(store, prefix, after string, limit int, stderr io.Writer) ([]remoteRow, int) {
	q := url.Values{}
	q.Set("store", store)
	q.Set("prefix", prefix)
	if after != "" {
		q.Set("after", after)
	}
	q.Set("limit", fmt.Sprintf("%d", limit))
	resp, err := rc.do("GET", rc.base+"?"+q.Encode(), nil)
	if err != nil {
		fmt.Fprintf(stderr, "bluedb: connect %s: %v\n", rc.base, err)
		return nil, 1
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return nil, rc.fail(resp, stderr)
	}
	var out struct {
		Rows []remoteRow `json:"rows"`
	}
	_ = json.NewDecoder(resp.Body).Decode(&out)
	return out.Rows, 0
}

func (rc *remoteClient) scan(store, prefix string, keysOnly bool, f flags, stdout, stderr io.Writer) int {
	rows, code := rc.fetchRows(store, prefix, "", f.limit, stderr)
	if code != 0 {
		return code
	}
	if f.json {
		enc := json.NewEncoder(stdout)
		for _, r := range rows {
			_ = enc.Encode(map[string]any{"key": r.Key, "value": jsonValue([]byte(r.Value))})
		}
		return 0
	}
	for _, r := range rows {
		if keysOnly {
			fmt.Fprintln(stdout, r.Key)
		} else {
			fmt.Fprintf(stdout, "%s\t%s\n", r.Key, displayValue([]byte(r.Value), false))
		}
	}
	return 0
}

func (rc *remoteClient) get(store, key string, f flags, stdout, stderr io.Writer) int {
	// The endpoint exposes prefix-scan; fetch the prefix bucket and match exactly.
	rows, code := rc.fetchRows(store, key, "", 1000, stderr)
	if code != 0 {
		return code
	}
	for _, r := range rows {
		if r.Key == key {
			if f.json {
				enc := json.NewEncoder(stdout)
				enc.SetIndent("", "  ")
				_ = enc.Encode(jsonValue([]byte(r.Value)))
			} else {
				fmt.Fprintln(stdout, displayValue([]byte(r.Value), f.raw))
			}
			return 0
		}
	}
	fmt.Fprintf(stderr, "bluedb: key %q not found\n", key)
	return 4
}

func (rc *remoteClient) mutate(store, op, key, value string, f flags, stdin io.Reader, stdout, stderr io.Writer) int {
	if op == "delete" && !f.yes && !confirm(stdin, stdout, fmt.Sprintf("delete key %q on %s (LIVE)?", key, store)) {
		fmt.Fprintln(stdout, "aborted")
		return 0
	}
	if f.stdin && op == "put" {
		b, _ := io.ReadAll(stdin)
		value = string(b)
	}
	body, _ := json.Marshal(map[string]string{"store": store, "op": op, "key": key, "value": value})
	resp, err := rc.do("POST", rc.base+"/mutate", body)
	if err != nil {
		fmt.Fprintf(stderr, "bluedb: connect %s: %v\n", rc.base, err)
		return 1
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return rc.fail(resp, stderr)
	}
	fmt.Fprintf(stdout, "%s %s on %s (live)\n", op, key, store)
	return 0
}
