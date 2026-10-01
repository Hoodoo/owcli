// Command owcli generates, grounds, and searches a repository wiki.
package main

import (
	"fmt"
	"os"

	"owcli/internal/cli"
)

func main() {
	if err := cli.NewRootCommand().Execute(); err != nil {
		fmt.Fprintln(os.Stderr, "owcli:", err)
		os.Exit(1)
	}
}
