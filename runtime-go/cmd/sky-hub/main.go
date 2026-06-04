// sky-hub — standalone executable for the Sky Console Hub. Built
// from the embedded runtime-go tree by `sky console-serve` (see
// app/Main.hs runConsoleServe) and exec'd with flags forwarded
// from the Haskell CLI driver.
//
// Lives under runtime-go/cmd/sky-hub/ so the same `go build` step
// that compiles the user's Sky.Live app can also produce this
// binary — no separate toolchain, no cross-compilation step at
// `sky` install time.
package main

import (
	"flag"
	"fmt"
	"os"

	"sky-app/rt/hub"
)

func main() {
	cfg := hub.ConfigFromEnv()

	flag.IntVar(&cfg.Port, "port", cfg.Port, "HTTP listen port")
	flag.StringVar(&cfg.DataDir, "data-dir", cfg.DataDir, "directory for the hub's SQLite database")
	flag.StringVar(&cfg.AuthMode, "auth", cfg.AuthMode, "auth mode: token | app | off")
	flag.StringVar(&cfg.TLSCert, "tls-cert", cfg.TLSCert, "TLS certificate file (paired with --tls-key)")
	flag.StringVar(&cfg.TLSKey, "tls-key", cfg.TLSKey, "TLS key file (paired with --tls-cert)")
	flag.Parse()

	if err := hub.Run(cfg); err != nil {
		fmt.Fprintf(os.Stderr, "sky-hub: %v\n", err)
		os.Exit(1)
	}
}
