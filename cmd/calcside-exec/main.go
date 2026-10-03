// calcside-exec runs one instance process standalone: it reads a
// subproc.ChildConfig from stdin and serves the runtime protocol on the
// configured unix socket until stdin closes. Workers normally re-exec
// their own binary (`calcside-worker __instance`) instead; this binary
// exists for running an instance process on its own.
package main

import (
	"fmt"
	"os"

	"calcside/internal/node/subproc"
)

func main() {
	if err := subproc.RunChild(os.Stdin, os.Stdout); err != nil {
		fmt.Fprintln(os.Stderr, "calcside-exec:", err)
		os.Exit(1)
	}
}
