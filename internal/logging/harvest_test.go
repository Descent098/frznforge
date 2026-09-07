package logging

import "testing"

// Masking an attribute and substituting a value everywhere are two different decisions, and this
// pins the gap between them.
//
// They started as one list, and an over-broad name was therefore not merely noisy but corrupting.
// `session` was in it; this machine sets CLAUDE_CODE_SESSION_ID; that value appears in every temp
// path here — so every path in the run log came out as
// `…\Temp\claude\<project>\***\scratchpad\…`, naming a directory that does not exist. A log whose
// paths are wrong is worse than a log without paths, because it sends the reader somewhere.
//
// The asymmetry is the point. Masking an attribute called `session` hides one field a reader did
// not need. Substituting its value reaches every string in the file, including ones that have
// nothing to do with credentials. So the second list has to be strictly narrower, and only names
// that cannot plausibly mean anything else belong in it.
func TestHarvestIsNarrowerThanMasking(t *testing.T) {
	t.Run("credentials are both masked and substituted", func(t *testing.T) {
		for _, name := range []string{
			"GITHUB_TOKEN", "GITLAB_TOKEN", "GH_PASSWORD", "MY_PAT", "FORGE_SECRET",
			"API_KEY", "SSH_KEY", "PRIVATE_KEY", "sshKey", "githubToken",
		} {
			if !SecretKey(name) {
				t.Errorf("%s: not masked, and it names a credential", name)
			}
			if !harvestEnvName(name) {
				t.Errorf("%s: not substituted, so its value would reach the log verbatim", name)
			}
		}
	})

	t.Run("identifiers are masked but never substituted", func(t *testing.T) {
		// Each of these names something a log legitimately prints in other contexts. Hiding the
		// attribute is fine; rewriting every occurrence of the value is not.
		for _, name := range []string{"CLAUDE_CODE_SESSION_ID", "SESSION_ID", "SESSIONID"} {
			if !SecretKey(name) {
				t.Errorf("%s: should still be masked as an attribute", name)
			}
			if harvestEnvName(name) {
				t.Errorf("%s: must NOT become a global substitution — this is the bug that "+
					"rewrote every path in the log", name)
			}
		}
	})

	t.Run("ordinary names are left alone entirely", func(t *testing.T) {
		for _, name := range []string{"PATH", "TEMP", "HOME", "KEYBOARD_LAYOUT", "MONKEY_MODE", "AUTHOR", "KEYWORDS"} {
			if harvestEnvName(name) {
				t.Errorf("%s: harvested, which would corrupt every record containing its value", name)
			}
		}
	})

	t.Run("a value shaped like a path is never substituted", func(t *testing.T) {
		// The second guard, independent of the name. Even a variable that really is a secret
		// cannot be allowed to rewrite text if its value is a path or contains whitespace: real
		// credentials are one opaque run of characters, and anything else is far likelier to be
		// something the log needs to print correctly.
		for _, v := range []string{
			`C:\Users\Kieran\Temp\abc-123`,
			"/home/kieran/.cache/frznforge",
			"has space here",
			"short",
			"",
		} {
			if harvestable(v) {
				t.Errorf("%q: harvestable, but it is not credential-shaped", v)
			}
		}
		for _, v := range []string{"ghp_ABCDEFGHIJKLMNOPQRSTUVWXYZ", "glpat-CUSTOMNAMEDSECRET99999"} {
			if !harvestable(v) {
				t.Errorf("%q: not harvestable, and it is exactly what this exists to remove", v)
			}
		}
	})
}
