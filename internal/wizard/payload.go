package wizard

// Validation for everything the browser posts about a repository.
//
// safeField — shared with entryFor, which applies it to listing-derived values — is what stops a
// typo or a hostile API response from putting nonsense into a file the user will read for years.
// renderEntry escaping quotes, backslashes and line terminators is the other half; neither is
// trusted to be the only one.

import (
	"fmt"
	"strings"
)

func asProvider(value any) (string, error) {
	name, ok := value.(string)
	if !ok || !knownProvider(name) {
		return "", badRequest(fmt.Sprintf("unknown provider: %q", value))
	}
	return name, nil
}

func asReleases(value any) (string, error) {
	switch value {
	case nil, "provider":
		return "provider", nil
	case "tags":
		return "tags", nil
	}
	return "", badRequest("releases must be 'provider' or 'tags'")
}

// asHost takes a host as the browser typed it: http(s) only, no credentials.
//
// assertSafeHost validates the TEXT, not just what a URL parser makes of it. A parser is
// permissive about things a config file is not — a line terminator inside a URL parses fine in
// most of them and would survive into a string literal in the file.
func asHost(value any, required bool) (string, error) {
	text, ok := value.(string)
	if !ok || strings.TrimSpace(text) == "" {
		if required {
			return "", badRequest("a host is required for this provider")
		}
		return "", nil
	}
	text = strings.TrimRight(strings.TrimSpace(text), "/")
	if _, err := assertSafeHost(text); err != nil {
		return "", badRequest(err.Error())
	}
	return text, nil
}

// asAccount takes a user, an organisation, or a GitLab group path.
//
// listRepos URL-encodes it, so a ".." cannot escape anything — but a config full of ../../etc is
// nobody's intent, so say no here rather than send it to a provider and report its 404.
func asAccount(value any) (string, error) {
	text, ok := value.(string)
	if !ok || strings.TrimSpace(text) == "" {
		return "", badRequest("an account is required")
	}
	text = strings.TrimSpace(text)
	if !safeField.MatchString(text) {
		return "", badRequest("account contains characters that cannot be in a path: " + text)
	}
	for _, segment := range strings.Split(text, "/") {
		if segment == "" || segment == "." || segment == ".." {
			return "", badRequest("not an account name: " + text)
		}
	}
	return text, nil
}

// asField validates one optional entry field. Absent stays absent; present must be safe.
func asField(value any, key string) (string, error) {
	if value == nil {
		return "", nil
	}
	text, ok := value.(string)
	if !ok || !safeField.MatchString(text) {
		return "", badRequest(fmt.Sprintf("invalid %s: %q", key, value))
	}
	return text, nil
}

// asEntry validates one browser-supplied config entry before it can reach the file.
func asEntry(value any) (RepoEntry, error) {
	raw, err := asRecord(value)
	if err != nil {
		return RepoEntry{}, err
	}
	entry := RepoEntry{}
	if entry.Type, err = asProvider(raw["type"]); err != nil {
		return RepoEntry{}, err
	}
	if entry.Host, err = asHost(raw["host"], false); err != nil {
		return RepoEntry{}, err
	}
	if entry.Owner, err = asField(raw["owner"], "owner"); err != nil {
		return RepoEntry{}, err
	}
	if entry.Repo, err = asField(raw["repo"], "repo"); err != nil {
		return RepoEntry{}, err
	}
	if entry.Project, err = asField(raw["project"], "project"); err != nil {
		return RepoEntry{}, err
	}
	if entry.Slug, err = asField(raw["slug"], "slug"); err != nil {
		return RepoEntry{}, err
	}
	if _, present := raw["releases"]; present {
		if entry.Releases, err = asReleases(raw["releases"]); err != nil {
			return RepoEntry{}, err
		}
	}
	if entry.Project == "" && (entry.Owner == "" || entry.Repo == "") {
		return RepoEntry{}, badRequest("an entry needs owner+repo, or project")
	}
	return entry, nil
}
