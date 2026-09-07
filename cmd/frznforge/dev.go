package main

// `frznforge dev` — serve the site the last build produced, and nothing else.
//
// This is the port of scripts/dev.ts and tests/e2e/serve.ts, which were the same server wearing
// two faces: one spawned `astro preview` for a person, the other was a 54-line node script for
// Playwright. They are one implementation now (internal/serve), so the server a person looks at
// and the server the e2e suite asserts against cannot drift apart.
//
// The command renders nothing. Everything below the flag parsing is about making that fact
// impossible to miss — the notice before the server starts, and a preflight that names the
// command to run when there is no build to serve. A stale `dist/` is the most common confusion
// this tool produces, and it costs eight lines of print to pre-empt.
//
import (
	"errors"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"

	"frznforge/internal/config"
	"frznforge/internal/serve"
	"frznforge/internal/timings"
)

// devDefaultPort is what `npm run dev` served on: scripts/dev.ts handed the site to `astro
// preview`, whose default port is 4321, and the README tells people to open it there.
const devDefaultPort = 4321

// devArgs is the parsed command line. Split out from devCmd so the parsing can be tested
// without binding a port.
type devArgs struct {
	Root string
	Dir  string
	Base string
	Host string
	Port int
	// DirSet records whether --dir was given. It decides whether this is "serve my project's
	// build" or "serve that directory": only the first has an artifact worth checking for.
	DirSet bool
	// BaseSet distinguishes `--base=` (serve at the root, overriding the config) from an absent
	// flag (inherit site.base).
	BaseSet bool
	Quiet   bool
}

func parseDevArgs(argv []string) (devArgs, error) {
	args := devArgs{Root: ".", Port: devDefaultPort}
	for _, a := range argv {
		switch {
		case strings.HasPrefix(a, "--dir="):
			args.Dir, args.DirSet = strings.TrimPrefix(a, "--dir="), true
		case strings.HasPrefix(a, "--root="):
			args.Root = strings.TrimPrefix(a, "--root=")
		case strings.HasPrefix(a, "--base="):
			args.Base, args.BaseSet = config.NormalizeBase(strings.TrimPrefix(a, "--base=")), true
		case strings.HasPrefix(a, "--host="):
			args.Host = strings.TrimPrefix(a, "--host=")
		case strings.HasPrefix(a, "--port="):
			raw := strings.TrimPrefix(a, "--port=")
			n, err := strconv.Atoi(raw)
			if err != nil || n < 0 || n > 65535 {
				return args, fmt.Errorf("dev: --port needs a number from 0 to 65535, got %q (0 asks the OS for a free one)", raw)
			}
			args.Port = n
		case a == "--quiet" || a == "-q":
			args.Quiet = true
		default:
			return args, fmt.Errorf("dev: unknown flag %q — it takes --dir=, --root=, --base=, --host=, --port= and --quiet", a)
		}
	}
	return args, nil
}

// devCmd serves the last build.
func devCmd(argv []string, io *Io, step *timings.Step) error {
	args, err := parseDevArgs(argv)
	if err != nil {
		return err
	}

	root, err := filepath.Abs(args.Root)
	if err != nil {
		return err
	}
	// The config supplies three things: where the artifact went, where the site was rendered to,
	// and the deploy base it was rendered for. A config that will not load is not this command's
	// problem to diagnose — `frznforge build` reports it properly — so fall back to the
	// documented defaults and let the preflight below say something actionable anyway.
	base, artifactDir := "", filepath.Join(root, "data")
	if cfg, err := config.Load(root); err == nil {
		base, artifactDir = cfg.Site.Base, cfg.OutDir
	}
	if args.BaseSet {
		base = args.Base
	}

	paths := serve.Paths{
		Root:         root,
		Dir:          filepath.Join(root, "dist"),
		ArtifactFile: filepath.Join(artifactDir, "forge.json"),
	}
	if args.DirSet {
		// An explicit --dir is "serve these files" — the e2e harness pointing at a fixture build,
		// or a copy of someone else's output. There is no project artifact behind it to check,
		// and complaining about a missing data/forge.json would be noise.
		paths.Dir, paths.ArtifactFile = args.Dir, ""
		if paths.Dir, err = filepath.Abs(args.Dir); err != nil {
			return err
		}
	}

	if check := serve.Preflight(paths, serve.Exists); !check.OK {
		return errors.New(strings.Join(check.Lines, "\n"))
	}
	if !args.Quiet {
		io.log(strings.Join(serve.Notice(paths), "\n"))
	}

	// Only the bind is timed. The serving that follows lasts until someone presses Ctrl-C, and a
	// step whose duration is "how long the developer left it open" would be the loudest row in
	// every aggregate while meaning nothing at all.
	bind := stepUnder(step, "serve.listen", paths.Dir)
	srv, err := serve.Listen(serve.Options{Dir: paths.Dir, Base: base, Host: args.Host, Port: args.Port})
	bind.Fail(err).Done()
	if err != nil {
		return err
	}
	// Always printed, --quiet included: with --port=0 this line is the only way anything — the
	// e2e harness above all — can find out where the server ended up.
	io.logf("serving %s on %s", srv.Dir, srv.URL)

	stop := make(chan os.Signal, 1)
	signal.Notify(stop, os.Interrupt)
	go func() {
		<-stop
		io.log("\nstopped")
		srv.Close()
	}()
	return srv.Serve()
}
