package config

import (
	"strings"
	"testing"
)

// The `postprocess` block's whole reason for living in this package is that ParseBytes decodes
// with DisallowUnknownFields: before the field existed, a config declaring the block failed to
// load with "unrecognised or mistyped setting", which told the user their config was wrong about
// the one thing it was right about. These tests are that gap, stated as behaviour.

// TestPostprocessBlockLoads — the block reaches Config with both fields intact.
func TestPostprocessBlockLoads(t *testing.T) {
	cfg, err := ParseBytes([]byte(`{
  "owner": { "name": "K", "handle": "k" },
  "postprocess": {
    "command": "npx esbuild --minify --outdir=dist dist/js/*.js", // runs over the finished site
    "dir": "tools/min"
  }
}`))
	if err != nil {
		t.Fatalf("a config with a postprocess block must load: %v", err)
	}
	if got := cfg.Postprocess.Command; got != "npx esbuild --minify --outdir=dist dist/js/*.js" {
		t.Errorf("postprocess.command is %q", got)
	}
	if got := cfg.Postprocess.Dir; got != "tools/min" {
		t.Errorf("postprocess.dir is %q", got)
	}
	if !cfg.Postprocess.Configured() {
		t.Error("a block with a command must report Configured")
	}
}

// TestPostprocessDefaultsToNothing — the documented default. A config that never mentions the
// hook must produce a block that runs nothing; anything else would start a process on every
// build of every existing site.
func TestPostprocessDefaultsToNothing(t *testing.T) {
	cfg, err := ParseBytes([]byte(`{ "owner": { "name": "K", "handle": "k" } }`))
	if err != nil {
		t.Fatalf("ParseBytes: %v", err)
	}
	if cfg.Postprocess.Configured() {
		t.Errorf("no block was declared, yet the hook reports configured: %+v", cfg.Postprocess)
	}
	if cfg.Postprocess != (PostprocessConfig{}) {
		t.Errorf("an undeclared block must be the zero value, got %+v", cfg.Postprocess)
	}
}

// TestPostprocessValidation — the loader has to say what is wrong and what to do, because the
// alternative (a generic decode failure, or silence) is what sends someone hunting through a
// config file for a mistake the loader already knows the shape of.
func TestPostprocessValidation(t *testing.T) {
	cases := []struct {
		name     string
		src      string
		wantWord string // a phrase the message must carry, so a rewrite cannot hollow it out
	}{
		{
			name:     "dir without a command would never run",
			src:      `{"owner":{"name":"K","handle":"k"},"postprocess":{"dir":"tools/min"}}`,
			wantWord: "nothing would run",
		},
		{
			name:     "a blank command is a half-deleted block",
			src:      `{"owner":{"name":"K","handle":"k"},"postprocess":{"command":"   "}}`,
			wantWord: "postprocess.command is blank",
		},
		{
			name:     "a backslash dir only works on Windows",
			src:      `{"owner":{"name":"K","handle":"k"},"postprocess":{"command":"echo hi","dir":"tools\\min"}}`,
			wantWord: "forward slashes",
		},
		{
			name:     "a misspelled key inside the block is still a mistyped setting",
			src:      `{"owner":{"name":"K","handle":"k"},"postprocess":{"cmd":"echo hi"}}`,
			wantWord: "cmd",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := ParseBytes([]byte(tc.src))
			if err == nil {
				t.Fatal("expected the load to fail")
			}
			if !strings.Contains(err.Error(), tc.wantWord) {
				t.Errorf("the message does not mention %q, so it does not tell the user what to fix:\n%v", tc.wantWord, err)
			}
		})
	}
}

// TestPostprocessAcceptsAnAbsoluteDir — the runner resolves a relative dir against the project
// root and takes an absolute one as written, so validation must not reject the second. A hook
// that shells out to a toolchain installed outside the project is a legitimate thing to ask for.
func TestPostprocessAcceptsAnAbsoluteDir(t *testing.T) {
	if err := ValidatePostprocess(PostprocessConfig{Command: "echo hi", Dir: "/opt/tools"}); err != nil {
		t.Errorf("an absolute POSIX dir must be accepted: %v", err)
	}
}
