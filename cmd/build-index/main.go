// Command build-index builds the three serialized index files offline, from
// the small test dataset or the full production datasets, and reports the
// time and memory each step costs. The steps themselves live in lib/builder,
// shared with the initializer's first boot.
package main

import (
	"fmt"
	"os"
)

func main() {
	if err := run(os.Args[1:], os.Stdout, os.Stderr); err != nil {
		fmt.Fprintf(os.Stderr, "build-index: %v\n", err)
		os.Exit(1)
	}
}
