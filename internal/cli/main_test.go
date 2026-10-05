package cli

import (
	"os"
	"testing"
)

// TestMain clears OWCLI_HOME, which overrides the XDG directories the tests
// set to isolate themselves from the developer's real bindings and wikis.
func TestMain(m *testing.M) {
	os.Unsetenv("OWCLI_HOME")
	os.Exit(m.Run())
}
