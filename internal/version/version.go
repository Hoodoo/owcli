// Package version identifies the owcli build.
package version

// Version is overridden at build time with
// -ldflags "-X owcli/internal/version.Version=<v>".
var Version = "0.0.0-dev"

// Producer is the OKF actor owcli stamps into provenance fields.
func Producer() string {
	return "owcli/" + Version
}
