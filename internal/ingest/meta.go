package ingest

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"path/filepath"
	"regexp"
	"strings"
	"unicode/utf16"

	"frznforge/internal/config"
	"frznforge/internal/model"
)

// MetaFilename is the per-repo metadata file, read from the default branch's tree — never
// from the working copy, so an uncommitted edit cannot change the artifact.
const MetaFilename = ".frznforge.json"

// MaxDescription is the artifact's description limit, counted in UTF-16 code units.
const MaxDescription = 300

// isDescriptionTooLong counts UTF-16 code units, because that is what `z.string().max(300)`
// counts. Counting code points instead would let 300 emoji (600 units) past this check and
// then fail artifact validation, taking the whole build down over someone else's metadata.
func isDescriptionTooLong(desc string) bool {
	return utf16Len(desc) > MaxDescription
}

func utf16Len(s string) int {
	n := 0
	for _, r := range s {
		n++
		if r > 0xffff {
			n++
		}
	}
	return n
}

// truncateDescription cuts to MaxDescription code units (299 + "…"), never between the two
// halves of a surrogate pair — a lone surrogate is valid in a JS string but mojibake wherever
// it is rendered.
func truncateDescription(desc string) string {
	if !isDescriptionTooLong(desc) {
		return desc
	}
	units := utf16.Encode([]rune(desc))
	end := MaxDescription - 1
	if end-1 < len(units) {
		if lead := units[end-1]; lead >= 0xd800 && lead <= 0xdbff {
			end--
		}
	}
	return string(utf16.Decode(units[:end])) + "…"
}

// RepoMetaResult is a parsed .frznforge.json plus whatever went wrong reading it.
type RepoMetaResult struct {
	Meta     *config.RepoMetaInput
	Warnings []model.Warning
}

// ReadRepoMetaFile reads and validates `<treeish>:.frznforge.json`.
//
// Missing means no metadata and no warning — most repos have none. Invalid JSON or a value of
// the wrong type is a `repo-meta-invalid` warning and no metadata: a repo publishing broken
// metadata must not fail the build of a whole forge. An over-long description is truncated
// with a `description-truncated` warning rather than rejected, since the rest of the file is
// still good.
func ReadRepoMetaFile(ctx context.Context, repo, treeish string) (RepoMetaResult, error) {
	res := RepoMetaResult{Warnings: []model.Warning{}}
	out, ok, err := GitMaybe(ctx, repo, "rev-parse", "--verify", "--quiet", treeish+":"+MetaFilename)
	if err != nil {
		return res, err
	}
	sha := strings.TrimSpace(out)
	if !ok || sha == "" {
		return res, nil
	}
	content, err := ReadBlob(ctx, repo, sha)
	if err != nil {
		return res, err
	}

	var anyValue any
	if err := json.Unmarshal(content, &anyValue); err != nil {
		res.Warnings = append(res.Warnings, model.Warning{
			Code:    "repo-meta-invalid",
			Repo:    nil,
			Message: fmt.Sprintf("%s is not valid JSON (%s); ignored", MetaFilename, err),
		})
		return res, nil
	}

	invalid := func(issues string) RepoMetaResult {
		// Deliberately replaces the warning list rather than appending: a file that fails
		// validation contributes nothing, truncation warning included.
		return RepoMetaResult{Meta: nil, Warnings: []model.Warning{{
			Code:    "repo-meta-invalid",
			Repo:    nil,
			Message: fmt.Sprintf("%s failed validation (%s); ignored", MetaFilename, issues),
		}}}
	}

	var meta config.RepoMetaInput
	dec := json.NewDecoder(strings.NewReader(string(content)))
	if err := dec.Decode(&meta); err != nil {
		return invalid(err.Error()), nil
	}
	if meta.Description != nil && isDescriptionTooLong(*meta.Description) {
		truncated := truncateDescription(*meta.Description)
		meta.Description = &truncated
		res.Warnings = append(res.Warnings, model.Warning{
			Code:    "description-truncated",
			Repo:    nil,
			Message: fmt.Sprintf("%s description exceeded %d characters and was truncated", MetaFilename, MaxDescription),
		})
	}
	if issues := validateRepoMeta(&meta); issues != "" {
		return invalid(issues), nil
	}
	res.Meta = &meta
	return res, nil
}

// validateRepoMeta applies the rules the zod schema applies, returning them joined the way
// zod's issue list is joined. The wording is ours — zod's exact message text is not
// reproducible here — but the accept/reject decision is the same.
func validateRepoMeta(m *config.RepoMetaInput) string {
	var issues []string
	if m.Name != nil && *m.Name == "" {
		issues = append(issues, "name: Too small: expected string to have >=1 characters")
	}
	if m.License != nil && *m.License == "" {
		issues = append(issues, "license: Too small: expected string to have >=1 characters")
	}
	if m.ReleaseMode != nil && *m.ReleaseMode != "tags" && *m.ReleaseMode != "provider" {
		issues = append(issues, fmt.Sprintf("releaseMode: Invalid option: expected one of \"tags\"|\"provider\", received %q", *m.ReleaseMode))
	}
	for i, t := range m.Tags {
		if t == "" {
			issues = append(issues, fmt.Sprintf("tags.%d: Too small: expected string to have >=1 characters", i))
		}
	}
	if m.Links != nil {
		for _, l := range []struct {
			key   string
			value *string
		}{
			{"homepage", m.Links.Homepage},
			{"issues", m.Links.Issues},
			{"donations", m.Links.Donations},
			{"upstream", m.Links.Upstream},
		} {
			if l.value == nil {
				continue
			}
			if u, err := url.Parse(*l.value); err != nil || u.Scheme == "" {
				issues = append(issues, "links."+l.key+": Invalid URL")
			}
		}
	}
	return strings.Join(issues, "; ")
}

// mergedMeta is the metadata the artifact actually records, after every layer has had its say.
type mergedMeta struct {
	Name        string
	Description *string
	Links       model.RepoLinks
	Tags        []string
	Template    bool
	// License is an SPDX override, "" when none was configured.
	License     string
	ReleaseMode string
}

// mergeMeta layers site-config overrides over the in-repo file over derived defaults.
func mergeMeta(defaultName string, fileMeta, overrides *config.RepoMetaInput) (mergedMeta, error) {
	if overrides != nil && overrides.Description != nil && isDescriptionTooLong(*overrides.Description) {
		return mergedMeta{}, fmt.Errorf(
			"config overrides.description for '%s' is longer than %d characters", defaultName, MaxDescription)
	}
	pickString := func(get func(*config.RepoMetaInput) *string) *string {
		if overrides != nil {
			if v := get(overrides); v != nil {
				return v
			}
		}
		if fileMeta != nil {
			return get(fileMeta)
		}
		return nil
	}

	out := mergedMeta{Name: defaultName, Tags: []string{}, ReleaseMode: "tags"}
	if v := pickString(func(m *config.RepoMetaInput) *string { return m.Name }); v != nil {
		out.Name = *v
	}
	out.Description = pickString(func(m *config.RepoMetaInput) *string { return m.Description })
	if v := pickString(func(m *config.RepoMetaInput) *string { return m.License }); v != nil {
		out.License = *v
	}
	if v := pickString(func(m *config.RepoMetaInput) *string { return m.ReleaseMode }); v != nil {
		out.ReleaseMode = *v
	}
	if overrides != nil && overrides.Tags != nil {
		out.Tags = append([]string{}, overrides.Tags...)
	} else if fileMeta != nil && fileMeta.Tags != nil {
		out.Tags = append([]string{}, fileMeta.Tags...)
	}
	if overrides != nil && overrides.Template != nil {
		out.Template = *overrides.Template
	} else if fileMeta != nil && fileMeta.Template != nil {
		out.Template = *fileMeta.Template
	}
	// links merge per key rather than being picked wholesale, which is what every other field
	// does: adding a `donations` link in the config must not delete the homepage the repo's own
	// file declared.
	out.Links = mergeLinks(linksOf(fileMeta), linksOf(overrides))
	return out, nil
}

func linksOf(m *config.RepoMetaInput) *config.RepoLinks {
	if m == nil {
		return nil
	}
	return m.Links
}

// mergeLinks builds the artifact links in schema key order — the key order IS the artifact's
// byte order, so it is rebuilt rather than copied.
func mergeLinks(low, high *config.RepoLinks) model.RepoLinks {
	pick := func(get func(*config.RepoLinks) *string) *string {
		if high != nil {
			if v := get(high); v != nil {
				return v
			}
		}
		if low != nil {
			return get(low)
		}
		return nil
	}
	return model.RepoLinks{
		Homepage:  pick(func(l *config.RepoLinks) *string { return l.Homepage }),
		Issues:    pick(func(l *config.RepoLinks) *string { return l.Issues }),
		Donations: pick(func(l *config.RepoLinks) *string { return l.Donations }),
		Upstream:  pick(func(l *config.RepoLinks) *string { return l.Upstream }),
	}
}

var (
	nonSlugRun = regexp.MustCompile(`[^a-z0-9]+`)
	slugRe     = regexp.MustCompile(`^[a-z0-9][a-z0-9-]*$`)
)

// Slugify turns a directory basename into a slug: lowercase, runs of anything else to '-',
// trimmed. An empty result becomes "repo" rather than an unusable empty path segment.
func Slugify(name string) string {
	s := nonSlugRun.ReplaceAllString(strings.ToLower(name), "-")
	s = strings.Trim(s, "-")
	if s == "" {
		return "repo"
	}
	return s
}

// SlugFor is a repo source's slug: the explicit one, else the slugified directory basename.
func SlugFor(absPath, explicit string) (string, error) {
	slug := explicit
	if slug == "" {
		slug = Slugify(RepoBasename(absPath))
	}
	if !slugRe.MatchString(slug) {
		return "", fmt.Errorf("invalid slug %q for %s", slug, absPath)
	}
	return slug, nil
}

// RepoBasename is the directory name; a bare repo named `foo.git` reports as `foo`.
func RepoBasename(absPath string) string {
	abs, err := filepath.Abs(absPath)
	if err != nil {
		abs = absPath
	}
	base := filepath.Base(abs)
	if strings.HasSuffix(base, ".git") && len(base) > 4 {
		return base[:len(base)-4]
	}
	return base
}
