package linker

import (
	"encoding/json"
	"errors"
	"os"
	"testing"
)

// errorLabel renders a linker error as script/golden_linker.rb records it.
func errorLabel(err error) string {
	var le *Error
	if errors.As(err, &le) {
		return le.Label()
	}
	return "other: " + err.Error()
}

type goldenCase struct {
	Name         string `json:"name"`
	Extension    string `json:"extension"`
	Source       string `json:"source"`
	SelectedText string `json:"selected_text"`
	Occurrence   int    `json:"occurrence"`
	Output       string `json:"output"`
	Error        string `json:"error"`
}

// TestGoldenCases replays inputs captured from the real Ruby SelectionLinker
// (script/golden_linker.rb) and demands identical output or error.
func TestGoldenCases(t *testing.T) {
	raw, err := os.ReadFile("../../testdata/golden/linker/cases.json")
	if err != nil {
		t.Fatal(err)
	}
	var cases []goldenCase
	if err := json.Unmarshal(raw, &cases); err != nil {
		t.Fatal(err)
	}
	if len(cases) < 111 {
		t.Fatalf("only %d golden cases", len(cases))
	}
	for _, c := range cases {
		t.Run(c.Name, func(t *testing.T) {
			got, err := Link(c.Source, c.Extension, c.SelectedText, c.Occurrence, "/x--expand-1.html")
			if c.Error != "" {
				if err == nil || errorLabel(err) != c.Error {
					t.Fatalf("want error %q, got %q (%v)", c.Error, got, err)
				}
				return
			}
			if err != nil || got != c.Output {
				t.Fatalf("got %q (%v)\nwant %q", got, err, c.Output)
			}
		})
	}
}
