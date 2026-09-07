package scaffold

import (
	"fmt"
	"path/filepath"
	"strings"
)

// The `frznforge new` command line.
//
// It is parsed here rather than in cmd/frznforge so that the whole command — arguments,
// refusals and printed output — is testable without a process. cmd/frznforge/new.go is only the
// wiring that supplies a cwd and a place to print.

// initOnlyFlags are init's options, listed so `new --print` is a message that says where the
// flag belongs rather than a bare "unknown option".
//
// Flags are per-command on purpose: accepting one that silently does nothing is the worst of
// the three possible behaviours.
var initOnlyFlags = map[string]string{
	"--provider": "", "--host": "", "--account": "", "--select": "", "--releases": "",
	"--config": "", "--print": "", "--yes": "", "--web": "", "--port": "", "--no-open": "",
}

// Command runs `frznforge new` end to end: parse the arguments, scaffold, print the report.
//
// args excludes the command word. cwd is what a relative <dir> resolves against and what the
// printed next steps are relative to. Every refusal comes back as an error whose text is the
// whole message the user should see.
func Command(args []string, cwd string, log func(string)) error {
	var (
		target string
		opts   = Options{Cwd: cwd}
		errs   []string
	)
	for _, arg := range args {
		switch {
		case arg == "--help" || arg == "-h":
			log(Usage)
			return nil
		case arg == "--force":
			opts.Force = true
		case arg == "--dry-run":
			opts.DryRun = true
		case strings.HasPrefix(arg, "-"):
			if _, ok := initOnlyFlags[flagName(arg)]; ok {
				errs = append(errs, fmt.Sprintf("%s is an init option: frznforge init %s", flagName(arg), flagName(arg)))
				continue
			}
			errs = append(errs, fmt.Sprintf("unknown option: %s (new takes --force, --dry-run)", flagName(arg)))
		case target == "":
			target = arg
		default:
			errs = append(errs, "unexpected argument: "+arg)
		}
	}
	if len(errs) > 0 {
		return fmt.Errorf("%s\n\n%s", strings.Join(errs, "\n  "), Usage)
	}
	if target == "" {
		return fmt.Errorf("new needs a directory: frznforge new my-site\n\n%s", Usage)
	}

	opts.Dir = target
	if !filepath.IsAbs(target) && cwd != "" {
		opts.Dir = filepath.Join(cwd, target)
	}
	_, err := Run(opts, log)
	return err
}

// flagName is the `--key` part of `--key=value`, so a message about `--port=9` names `--port`.
func flagName(arg string) string {
	if i := strings.IndexByte(arg, '='); i != -1 {
		return arg[:i]
	}
	return arg
}
