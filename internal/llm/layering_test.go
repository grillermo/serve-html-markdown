package llm

import (
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// goListDeps returns every package pkg depends on, itself included.
func goListDeps(t *testing.T, pkg string) []string {
	t.Helper()
	goBin := filepath.Join(runtime.GOROOT(), "bin", "go")
	cmd := exec.Command(goBin, "list", "-deps", pkg)
	cmd.Dir = "."
	out, err := cmd.Output()
	if err != nil {
		var stderr []byte
		if ee, ok := err.(*exec.ExitError); ok {
			stderr = ee.Stderr
		}
		t.Fatalf("go list -deps %s: %v\n%s", pkg, err, stderr)
	}
	return strings.Fields(string(out))
}

func hasDep(deps []string, prefix string) string {
	for _, d := range deps {
		if d == prefix || strings.HasPrefix(d, prefix+"/") {
			return d
		}
	}
	return ""
}

// The layering the packages rely on: llm stays light (the Google API client,
// grpc and OpenTelemetry belong to the YouTube uploader in internal/video), the
// caption reduction is a leaf, and video never reaches the database or llm.
func TestLayering(t *testing.T) {
	const mod = "serve-html-markdown/internal/"
	for pkg, forbidden := range map[string][]string{
		"./":            {"google.golang.org/api", "google.golang.org/grpc", "go.opentelemetry.io", "golang.org/x/oauth2", mod + "video", mod + "store"},
		"../transcript": {"google.golang.org", "go.opentelemetry.io", mod + "video", mod + "llm", mod + "store"},
		"../video":      {mod + "store", mod + "llm", mod + "files"},
	} {
		deps := goListDeps(t, pkg)
		for _, f := range forbidden {
			if d := hasDep(deps, f); d != "" {
				t.Errorf("%s depends on %s (forbidden: %s)", pkg, d, f)
			}
		}
	}
}
