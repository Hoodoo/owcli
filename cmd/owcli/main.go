// Command owcli generates, grounds, and searches a repository wiki.
package main

import (
	"errors"
	"fmt"
	"os"

	"github.com/Hoodoo/owcli/internal/cli"
)

func main() {
	if err := cli.NewRootCommand().Execute(); err != nil {
		if !errors.Is(err, cli.ErrReported) {
			fmt.Fprintln(os.Stderr, "owcli:", err)
		}
		os.Exit(1)
	}
}
