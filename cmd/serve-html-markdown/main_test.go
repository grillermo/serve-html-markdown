package main

import (
	"bytes"
	"strings"
	"testing"
)

func TestRunExitCodes(t *testing.T) {
	cases := []struct {
		args []string
		code int
		out  string // substring expected in stdout
		err  string // substring expected in stderr
	}{
		{[]string{"-h"}, 0, "usage:", ""},
		{[]string{"--help"}, 0, "usage:", ""},
		{[]string{"help"}, 0, "usage:", ""},
		{[]string{"youtube-reauth-url"}, 1, "", "youtube-reauth-url: not implemented"},
		{[]string{"sync-files"}, 1, "", "sync-files: not implemented"},
		{[]string{"bogus"}, 2, "", "unknown command"},
	}
	for _, c := range cases {
		var out, errb bytes.Buffer
		if got := runTo(c.args, &out, &errb); got != c.code {
			t.Errorf("%v: exit %d, want %d", c.args, got, c.code)
		}
		if !strings.Contains(out.String(), c.out) {
			t.Errorf("%v: stdout %q missing %q", c.args, out.String(), c.out)
		}
		if !strings.Contains(errb.String(), c.err) {
			t.Errorf("%v: stderr %q missing %q", c.args, errb.String(), c.err)
		}
	}
}

func TestRunServeDefaultExitsZero(t *testing.T) {
	if got := run(nil); got != 0 {
		t.Fatalf("exit %d", got)
	}
}
