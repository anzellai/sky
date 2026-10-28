// Command fakegcp serves the runtime's Google Cloud emulator
// (rt/ai_gcpfake.go) on a loopback port, for the cargo flows where several
// Sky processes (an app, `sky ai` verbs) share one emulated project:
//
//	fakegcp -addr 127.0.0.1:0 -port-file <path>
//
// It writes the chosen address to -port-file, then serves until killed.
// Point a program at it with SKY_AI_GCP_ENDPOINT=http://<addr>.
package main

import (
	"flag"
	"fmt"
	"net"
	"net/http"
	"os"

	"sky-app/rt"
)

func main() {
	addr := flag.String("addr", "127.0.0.1:0", "listen address")
	portFile := flag.String("port-file", "", "write the listen address here")
	flag.Parse()
	ln, err := net.Listen("tcp", *addr)
	if err != nil {
		fmt.Fprintln(os.Stderr, "fakegcp:", err)
		os.Exit(1)
	}
	if *portFile != "" {
		tmp := *portFile + ".tmp"
		if err := os.WriteFile(tmp, []byte(ln.Addr().String()), 0o644); err != nil {
			fmt.Fprintln(os.Stderr, "fakegcp:", err)
			os.Exit(1)
		}
		os.Rename(tmp, *portFile)
	}
	fmt.Fprintln(os.Stderr, "fakegcp: listening on", ln.Addr())
	if err := http.Serve(ln, rt.FakeGCPHandler()); err != nil {
		fmt.Fprintln(os.Stderr, "fakegcp:", err)
		os.Exit(1)
	}
}
