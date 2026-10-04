// Command schemadump prints the SQLite DDL of the gormstore row models.
// atlas.hcl uses it as the desired schema for `atlas migrate diff`.
package main

import (
	"context"
	"fmt"
	"os"

	"calcside/internal/store/gormstore"
)

func main() {
	ddl, err := gormstore.ModelDDL(context.Background())
	if err != nil {
		fmt.Fprintln(os.Stderr, "schemadump:", err)
		os.Exit(1)
	}
	fmt.Print(ddl)
}
