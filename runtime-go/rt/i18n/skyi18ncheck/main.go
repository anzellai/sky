// Command skyi18ncheck is the build-time check of Std.I18n catalogues. The
// compiler builds it from the runtime it embeds and runs it during `sky check`
// and `sky build`, so the message parser of the runtime is the only parser:
// the build and the program can never read a message differently.
//
// Input (stdin): i18n.CheckConfig as JSON. Output (stdout): i18n.CheckResult
// as JSON. The exit status is 0 when the check ran, whatever it found.
package main

import (
	"encoding/json"
	"fmt"
	"io"
	"os"

	"sky-app/rt/i18n"
)

func main() {
	in, err := io.ReadAll(os.Stdin)
	if err != nil {
		fmt.Fprintln(os.Stderr, "skyi18ncheck: read stdin:", err)
		os.Exit(2)
	}
	var cfg i18n.CheckConfig
	if err := json.Unmarshal(in, &cfg); err != nil {
		fmt.Fprintln(os.Stderr, "skyi18ncheck: bad config:", err)
		os.Exit(2)
	}
	out, err := json.Marshal(i18n.Check(cfg))
	if err != nil {
		fmt.Fprintln(os.Stderr, "skyi18ncheck: encode:", err)
		os.Exit(2)
	}
	os.Stdout.Write(out)
}
