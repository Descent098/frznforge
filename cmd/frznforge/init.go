package main

// `frznforge init` — pick repositories from a hosting provider and add them to the config.
//
// The port of scripts/cli.ts's init command. Three rules survive the move to Go unchanged, and
// they are the reason this command is shaped the way it is:
//
//   - **Tokens are read from the environment and never written anywhere.** See listing.go.
//   - **The config file is edited textually.** See entries.go.
//   - **Never hang.** When stdin is not a terminal and the flags do not fully describe the run,
//     print how to do it non-interactively and stop. A tool that blocks on an unanswerable
//     question in CI looks like a hang, and the log says nothing.
//
// `--web` hands the whole flow to internal/wizard instead: the same steps in a browser, plus the
// settings a terminal picker has no good way to offer.

import (
	"errors"
	"fmt"
	"path/filepath"
	"strconv"
	"strings"

	"frznforge/internal/config"
	"frznforge/internal/wizard"
)

// initUsage is `frznforge init`'s own help. main.go's usage lists the command; this is the
// detail, printed for `frznforge init --help` and for a bad flag.
func initUsage() string {
	return `frznforge init — add repositories from a hosting provider

Usage
  frznforge init [options]

Options
  --provider=<github|gitlab|gitea|forgejo>   Provider to query.
  --host=<url>                               API/instance base URL (required for gitea/forgejo).
  --account=<name>                           User, organisation or GitLab group path.
  --select=<spec>                            Which of the listed repos to add:
                                             ` + selectSyntax() + `
                                             all-<flags> keeps everything except the matches
                                             (` + excludeHelp() + `).
                                             Combine them: ` + excludeCombinedExample() + `.
  --releases=<provider|tags>                 Where releases come from (default: provider).
  --config=<path>                            Config file to edit (default: the nearest
                                             ` + config.Filename + ` at or above the cwd).
  --print                                    Print the snippet only; never write a file.
  --yes                                      Write without the confirmation prompt.
  --web                                      Edit the config in a local browser UI: pick repos,
                                             plus site/owner/theme/ingest settings, orgs, hosted
                                             sites and profile.md. Interactive: it needs a
                                             browser, so it is not for CI (and it cannot be
                                             combined with --print).
  --port=<n>                                 Port for --web (default: a free one).
  --no-open                                  Do not launch a browser for --web; print the URL.
  --root=<dir>                               Directory the run is relative to (default: the cwd).
  --help, -h                                 Show this message.

Anything the flags do not answer is asked at the prompt. A run that names --provider, --account
and --select asks nothing, so it works in a script.

Tokens are read from the environment only and are never written to the config:
  GitHub   FRZNFORGE_GITHUB_TOKEN  or GITHUB_TOKEN    scope: public_repo (repo for private)
  GitLab   FRZNFORGE_GITLAB_TOKEN  or GITLAB_TOKEN    scope: read_api
  Gitea    FRZNFORGE_GITEA_TOKEN   or GITEA_TOKEN     scope: read:repository
  Forgejo  FRZNFORGE_FORGEJO_TOKEN or FORGEJO_TOKEN   scope: read:repository

Without a token only public repositories are listed. See docs/user/importing.md.`
}

// nonTTYMessage is what a piped `frznforge init` says instead of blocking on a prompt.
func nonTTYMessage() string {
	return `frznforge init is interactive, but stdin is not a terminal.

Run it from a terminal:
  frznforge init

…or describe the whole run with flags (works in CI and scripts):
  frznforge init --provider=github --account=YOU --select=all --print
  frznforge init --provider=forgejo --host=https://codeberg.org --account=YOU --select=1-3 --yes

…or add the entries to ` + config.Filename + ` by hand:
  "repos": [
    { "type": "github", "owner": "YOU", "repo": "REPO", "releases": "provider" },
  ]

See docs/user/importing.md for the full reference.`
}

// initFlags is `init`'s command line.
type initFlags struct {
	Provider string
	Host     string
	Account  string
	Select   string
	// SelectSet distinguishes `--select=` (an empty selection, which is a legitimate answer
	// meaning "none") from no --select at all (ask me).
	SelectSet bool
	Releases  string
	// ReleasesSet decides whether the releases question is asked at all.
	ReleasesSet bool
	Config      string
	Root        string
	Print       bool
	Yes         bool
	Help        bool
	Web         bool
	Port        int
	NoOpen      bool
}

// valueFlags are the options that take a value, in `--key=value` or `--key value` form.
var valueFlags = map[string]bool{
	"provider": true, "host": true, "account": true, "select": true,
	"releases": true, "config": true, "port": true, "root": true,
}

// parseInitFlags reads init's arguments.
//
// Bad input is collected rather than returned at the first offence: someone who mistyped two
// flags should see both, not discover the second one after fixing the first.
func parseInitFlags(argv []string) (initFlags, []string) {
	flags := initFlags{}
	var errs []string
	raw := map[string]string{}

	for i := 0; i < len(argv); i++ {
		arg := argv[i]
		if arg == "-h" {
			flags.Help = true
			continue
		}
		if !strings.HasPrefix(arg, "--") {
			errs = append(errs, "unexpected argument: "+arg+" (init takes options only)")
			continue
		}
		body := arg[2:]
		key, value, hasValue := strings.Cut(body, "=")

		switch key {
		case "print", "yes", "help", "web", "no-open":
			if hasValue && value != "true" && value != "false" {
				errs = append(errs, "--"+key+" does not take a value")
				continue
			}
			on := value != "false"
			switch key {
			case "print":
				flags.Print = on
			case "yes":
				flags.Yes = on
			case "help":
				flags.Help = on
			case "web":
				flags.Web = on
			case "no-open":
				flags.NoOpen = on
			}
			continue
		}
		if !valueFlags[key] {
			errs = append(errs, "unknown option: --"+key)
			continue
		}
		if !hasValue {
			// `--account YOU` as well as `--account=YOU`. A following `--flag` is never a value:
			// it is a forgotten argument, and swallowing it would silently drop a real flag.
			if i+1 >= len(argv) || strings.HasPrefix(argv[i+1], "--") {
				errs = append(errs, "--"+key+" needs a value")
				continue
			}
			value = argv[i+1]
			i++
		}
		raw[key] = value
	}

	if v, ok := raw["provider"]; ok {
		if knownProvider(v) {
			flags.Provider = v
		} else {
			errs = append(errs, fmt.Sprintf("--provider must be one of %s (got %q)", strings.Join(providerNames, ", "), v))
		}
	}
	if v, ok := raw["releases"]; ok {
		if v == "provider" || v == "tags" {
			flags.Releases, flags.ReleasesSet = v, true
		} else {
			errs = append(errs, fmt.Sprintf("--releases must be 'provider' or 'tags' (got %q)", v))
		}
	}
	if v, ok := raw["port"]; ok {
		n, err := strconv.Atoi(v)
		if err == nil && n >= 0 && n <= 65535 {
			flags.Port = n
		} else {
			errs = append(errs, fmt.Sprintf("--port must be a whole number between 0 and 65535 (got %q)", v))
		}
	}
	if v, ok := raw["host"]; ok {
		flags.Host = strings.TrimRight(v, "/")
	}
	if v, ok := raw["account"]; ok {
		flags.Account = strings.TrimSpace(v)
	}
	if v, ok := raw["select"]; ok {
		flags.Select, flags.SelectSet = v, true
	}
	flags.Config = raw["config"]
	flags.Root = raw["root"]

	// --print is the "never touch anything, just show me" form and --web is a browser session
	// that exists to write the config: honouring both would mean silently picking one.
	if flags.Web && flags.Print {
		errs = append(errs, "--web and --print cannot be combined: --web opens a browser UI that writes the config, "+
			"--print only prints a snippet. Drop one of them.")
	}
	return flags, errs
}

// initPlan is a run that needs no further questions.
type initPlan struct {
	provider string
	host     string
	account  string
	releases string
}

// planFromFlags answers: can this run go ahead without asking anything? The missing list is what
// decides whether a non-TTY run has to bail out, and it names the flags that would fix it.
func planFromFlags(flags initFlags) (initPlan, []string) {
	var missing []string
	if flags.Provider == "" {
		missing = append(missing, "--provider")
	}
	info := providers[flags.Provider]
	host := flags.Host
	if host == "" {
		host = info.defaultHost
	}
	if flags.Provider != "" && info.hostRequired && flags.Host == "" {
		missing = append(missing, "--host")
	}
	if flags.Account == "" {
		missing = append(missing, "--account")
	}
	if !flags.SelectSet {
		missing = append(missing, "--select")
	}
	if len(missing) > 0 || flags.Provider == "" || host == "" {
		return initPlan{}, missing
	}
	releases := flags.Releases
	if releases == "" {
		releases = "provider"
	}
	return initPlan{
		provider: flags.Provider,
		host:     host,
		account:  flags.Account,
		releases: releases,
	}, nil
}

// describeRepo is one line of the numbered listing.
func describeRepo(repo remoteRepo) string {
	var badges []string
	if repo.Private {
		badges = append(badges, "private")
	}
	if repo.Archived != nil && *repo.Archived {
		badges = append(badges, "archived")
	}
	if repo.Fork != nil && *repo.Fork {
		badges = append(badges, "fork")
	}
	line := repo.FullName
	if len(badges) > 0 {
		line += " (" + strings.Join(badges, ", ") + ")"
	}
	if repo.Description != "" {
		desc := repo.Description
		// Truncated by bytes, not runes, in the TypeScript. Runes here: cutting a UTF-8
		// sequence in half would put a replacement character on the user's screen.
		if r := []rune(desc); len(r) > 70 {
			desc = string(r[:70])
		}
		line += " — " + desc
	}
	return line
}

// askForSelection is the interactive "which of these?" loop, split out from runInit so it can be
// driven without a terminal — ask and write are the seam a test drives it through.
//
// It re-asks only when the answer could not be honoured: a spec that failed to parse, or a
// filter that excluded every repository. A spec that PARSED and deliberately chose nothing
// (`none`) has to end the loop — it is in the grammar the prompt advertises, and re-asking made
// it unanswerable, with Ctrl-C the only way out.
func askForSelection(
	repos []remoteRepo,
	ask func(question, fallback string) (string, error),
	write func(string),
) ([]remoteRepo, error) {
	for {
		spec, err := ask("Select repositories ("+selectSyntax()+")", "all")
		if err != nil {
			return nil, err
		}
		outcome, err := resolveSelection(repos, spec)
		if err != nil {
			write("  " + err.Error() + ".")
			continue
		}
		if len(outcome.repos) == 0 && outcome.filtered {
			write("  " + excludedEverythingMessage(spec, outcome))
			continue
		}
		if summary := selectionSummary(outcome); summary != "" {
			write("  " + summary)
		}
		return outcome.repos, nil
	}
}

// initCmd is the command entry point.
func initCmd(argv []string, io *Io) error {
	flags, errs := parseInitFlags(argv)
	if flags.Help {
		io.log(initUsage())
		return nil
	}
	if len(errs) > 0 {
		return fmt.Errorf("%s\n\n%s", strings.Join(errs, "\n  "), initUsage())
	}

	root := io.Cwd
	if flags.Root != "" {
		root = resolveAgainst(io.Cwd, flags.Root)
	}
	configPath := ""
	if flags.Config != "" {
		configPath = resolveAgainst(io.Cwd, flags.Config)
	}

	if flags.Web {
		// The browser UI is a whole other front end for the same steps, and it offers settings
		// the terminal picker deliberately does not (theme, orgs, profile.md).
		_, err := wizard.Serve(wizard.Options{
			Root:       root,
			ConfigPath: configPath,
			Port:       flags.Port,
			NoOpen:     flags.NoOpen,
			Out:        io.Out,
			Provider:   flags.Provider,
			Host:       flags.Host,
			Account:    flags.Account,
			Releases:   flags.Releases,
			Env:        io.Env,
			Client:     io.Client,
			Now:        io.Now,
		})
		return err
	}

	err := runInit(flags, root, configPath, io)
	// A question that ran out of input means this was not an interactive run after all, whatever
	// IsTTY believed. It believes wrong at least once on Windows — `frznforge init < NUL` hands
	// over a character device, which is exactly what the terminal check looks for — so the
	// guidance a non-TTY run gets up front is repeated here rather than being reachable only
	// through a heuristic that can be fooled.
	if errors.Is(err, errInputClosed) {
		return fmt.Errorf("%w.\n\n%s", err, nonTTYMessage())
	}
	return err
}

// resolveAgainst makes a user-supplied path absolute against the run's working directory.
func resolveAgainst(cwd, path string) string {
	if filepath.IsAbs(path) || cwd == "" {
		return path
	}
	return filepath.Join(cwd, path)
}

func runInit(flags initFlags, root, configPath string, io *Io) error {
	plan, missing := planFromFlags(flags)
	planned := len(missing) == 0
	// Everything the run needs is on the command line → no prompts, no terminal required.
	if !planned && !io.IsTTY {
		return errors.New(nonTTYMessage())
	}
	p := newPrompter(io)

	if !planned {
		p.write("frznforge init — add repositories from a hosting provider.")
		p.write("")
		provider := flags.Provider
		if provider == "" {
			choices := make([]choice, len(providerNames))
			for i, name := range providerNames {
				choices[i] = choice{value: name, label: providers[name].label, hint: providers[name].tokenScopes}
			}
			var err error
			if provider, err = p.choose("Provider", choices, 0); err != nil {
				return err
			}
		}
		info := providers[provider]
		host := flags.Host
		if host == "" {
			if info.hostRequired {
				answer, err := p.askRequired(info.label+" instance URL", info.hostSuggestion)
				if err != nil {
					return err
				}
				host = strings.TrimRight(answer, "/")
			} else {
				host = info.defaultHost
			}
		}
		account := flags.Account
		if account == "" {
			var err error
			if account, err = p.askRequired("Account ("+info.accountLabel+")", ""); err != nil {
				return err
			}
		}
		releases := flags.Releases
		if releases == "" {
			releases = "provider"
		}
		plan = initPlan{provider: provider, host: host, account: account, releases: releases}
	}

	info := providers[plan.provider]
	status := statusFor(plan.provider, io.Env)
	io.logf("Provider: %s (%s)", info.label, plan.host)
	io.log(status.message())
	io.logf("Listing repositories for %s…", plan.account)

	repos, err := listRepos(listRequest{
		provider: plan.provider,
		host:     plan.host,
		account:  plan.account,
		token:    status.token,
		client:   io.Client,
		warn:     func(line string) { io.errf("%s", line) },
	})
	if err != nil {
		return err
	}
	if len(repos) == 0 {
		hint := ""
		if status.from == "" {
			hint = " A token may reveal private ones."
		}
		return fmt.Errorf("no repositories visible for %s.%s", plan.account, hint)
	}

	var picked []remoteRepo
	if flags.SelectSet {
		outcome, err := resolveSelection(repos, flags.Select)
		if err != nil {
			return fmt.Errorf("--select: %w", err)
		}
		if summary := selectionSummary(outcome); summary != "" {
			io.log(summary)
		}
		if len(outcome.repos) == 0 && outcome.filtered {
			return errors.New(excludedEverythingMessage(flags.Select, outcome))
		}
		picked = outcome.repos
	} else {
		p.write("")
		for i, r := range repos {
			p.write(fmt.Sprintf("  %3d. %s", i+1, describeRepo(r)))
		}
		p.write("")
		p.write("  all-<flags> keeps everything except the matches — " + excludeHelp() +
			"; combine: " + excludeCombinedExample())
		if picked, err = askForSelection(repos, p.ask, p.write); err != nil {
			return err
		}
	}

	// Only ask about releases when the run was not already fully described by flags, and never
	// when --print was asked for: both of those are the scriptable forms, and blocking on a
	// prompt there would make a terminal behave differently from CI over the same command line.
	if !flags.ReleasesSet && io.IsTTY && !planned && !flags.Print {
		answer, err := p.choose("Where should releases come from?", []choice{
			{value: "provider", label: info.label + " releases", hint: "imported over the API"},
			{value: "tags", label: "annotated git tags", hint: "no API calls, works offline"},
		}, 0)
		if err != nil {
			return err
		}
		plan.releases = answer
	}

	if len(picked) == 0 {
		io.log("Nothing selected — no changes made.")
		return nil
	}

	entries, err := entriesFor(plan.provider, plan.host, picked, plan.releases)
	if err != nil {
		return err
	}
	io.log("")
	io.log(renderSnippet(entries))
	io.log("")

	if flags.Print {
		return nil
	}

	configFile := configPath
	if configFile == "" {
		configFile = findConfigFile(root)
	}
	if configFile == "" {
		io.logf("No %s found — paste the snippet above into your config.", config.Filename)
		return nil
	}

	_, source, err := readConfigSource(configFile)
	if err != nil {
		io.logf("Could not read %s (%v) — paste the snippet above into your config.", configFile, err)
		return nil
	}
	preview, ok := insertRepos(source, entries)
	if !ok {
		io.logf("Could not find a \"repos\": [ … ] array in %s — paste the snippet above by hand.", configFile)
		return nil
	}
	if !preview.changed {
		io.logf("All %s already in %s. Nothing to do.", plural(len(entries), "entry is", "entries are"), configFile)
		return nil
	}

	// Show exactly what is about to happen before asking. The confirmation is worth nothing if
	// the answer is "yes" to an unnamed change.
	io.log(configFile)
	for _, entry := range preview.added {
		io.log("  + " + renderEntry(entry) + ",")
	}
	for _, entry := range preview.skipped {
		io.log("  = " + renderEntry(entry) + ",   (already present, skipped)")
	}
	io.log("  a timestamped .bak copy is written first.")
	io.log("")

	confirmed := flags.Yes
	if !confirmed && io.IsTTY {
		if confirmed, err = p.confirm("Add "+plural(len(preview.added), "entry", "entries")+" to the config?", false); err != nil {
			return err
		}
	}
	if !confirmed {
		io.log("Not written. Re-run with --yes, or paste the snippet above.")
		return nil
	}

	written, ok, err := updateConfigFile(configFile, entries, io.clock())
	if err != nil {
		return err
	}
	if !ok || !written.changed {
		io.log("Nothing written.")
		return nil
	}
	io.log("Backup: " + written.backup)
	io.logf("Wrote %s to %s.", plural(len(written.added), "entry", "entries"), configFile)
	io.log("Next: frznforge build   (remote repos are mirror-cloned into the cache directory on the first run)")
	return nil
}

// plural is `1 entry` / `3 entries`, for the several messages that count what was added.
func plural(n int, one, many string) string {
	if n == 1 {
		return fmt.Sprintf("%d %s", n, one)
	}
	return fmt.Sprintf("%d %s", n, many)
}
