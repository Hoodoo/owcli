// Package version identifies the owcli build.
package version

import "runtime/debug"

// Version is overridden at build time with
// -ldflags "-X github.com/Hoodoo/owcli/internal/version.Version=<v>".
// A plain "go install …@<v>" sets no ldflags; init then falls back to the
// module version Go records in the binary.
var Version = "0.0.0-dev"

func init() {
	if Version != "0.0.0-dev" {
		return
	}
	if bi, ok := debug.ReadBuildInfo(); ok && bi.Main.Version != "" && bi.Main.Version != "(devel)" {
		Version = bi.Main.Version
	}
}

// Producer is the OKF actor owcli stamps into provenance fields.
func Producer() string {
	return "owcli/" + Version
}
