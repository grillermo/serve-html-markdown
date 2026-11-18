package files

import (
	"os"
	"testing"
)

// SetLinkFile replaces the hard-link function of CreateNew until the test ends.
func SetLinkFile(t testing.TB, f func(r *os.Root, oldname, newname string) error) {
	t.Helper()
	prev := linkFile
	linkFile = f
	t.Cleanup(func() { linkFile = prev })
}
