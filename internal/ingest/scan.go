package ingest

import (
	"context"
	"fmt"
	"sort"
	"strconv"

	"frznforge/internal/config"
	"frznforge/internal/model"
	"frznforge/internal/routes"
)

// ScanSource is one repository to scan, plus the metadata layers the caller resolved for it.
type ScanSource struct {
	// AbsPath is the directory to read: a checkout, or a bare mirror for a remote source.
	AbsPath string
	// Slug overrides the slug derived from the directory name.
	Slug string
	// DefaultName is the display name when no metadata layer sets one. Empty means "the repo
	// directory basename"; remote sources pass the configured name instead, because their
	// directory is a sanitised cache key rather than the repo's real name.
	DefaultName string
	// Overrides is the site config's `overrides` block — the highest metadata layer.
	Overrides *config.RepoMetaInput
	// ProviderMeta is metadata reported by a hosting provider: the LOWEST of the three layers
	// (config overrides > the repo's committed .frznforge.json > this).
	ProviderMeta *config.RepoMetaInput
	// Source is the artifact's `source` value; nil means a local repo at AbsPath.
	Source *model.RepoSource
	// ReleaseMode applies when neither .frznforge.json nor Overrides picks one. "" means tags.
	ReleaseMode string
	// Releases are provider-imported; kept only when the effective mode is "provider".
	Releases []model.Release
	// HostedRequests are the branches `hosting.sites` wants served from this repo; a nil entry
	// means "resolve automatically" (gh-pages → main → master).
	//
	// A resolved hosted branch always gets a browsable tree — exempt from the BranchTrees cap —
	// scanned with HostedMaxFileBytes instead of MaxBlobBytes, because a built site's bundles
	// routinely exceed the blob cap and an unstored file is a silent 404 on the hosted site.
	HostedRequests []*string
}

// ScanOptions are the ingest knobs one scan reads.
//
// The zero value is NOT the set of TypeScript defaults — Archives, in particular, defaults to
// true there and false here. Build it with ScanOptionsFromConfig rather than by hand; the
// resolved config has already applied every default.
type ScanOptions struct {
	MaxBlobBytes int64
	// MaxCommits caps commits stored per branch; nil = all.
	MaxCommits *int64
	// MaxCommitAgeDays keeps only commits from the last N days, anchored to the repo's newest
	// branch-head commit date — never to the clock. nil = no limit.
	MaxCommitAgeDays *int64
	// TagTrees is how many of the newest tags get browsable trees and archives.
	TagTrees int
	// BranchTrees is how many NON-default branches get browsable trees, most recently updated
	// first. BranchTreesAll overrides it with "every branch".
	//
	// The default branch always has a tree (Repo.tree/Repo.files) and never counts against the
	// cap. Tree/blob/raw pages are emitted per browsable ref, so this is the main control on
	// build size.
	BranchTrees    int
	BranchTreesAll bool
	// Archives produces zip source archives with `git archive`.
	Archives bool
	Insights InsightsOptions
	// HostedMaxFileBytes is the byte cap for files on hosted branches (hosting.maxFileBytes).
	HostedMaxFileBytes int64
	// Contributors indexes the configured contributors by lower-cased email. A match decorates
	// and merges the git-derived entry; absent means "git only", which is the normal case.
	//
	// Part of ScanOptions — and therefore of the scan-cache digest — on purpose: renaming a
	// contributor or adding an avatar changes the artifact, so a cached scan must not replay
	// the old value.
	Contributors ContributorIndex
}

// ScanOptionsFromConfig maps a resolved site config onto the scan knobs, so the two cannot
// drift apart and no caller has to remember which defaults are true.
func ScanOptionsFromConfig(cfg *config.Resolved) (ScanOptions, error) {
	branchTrees, all, err := cfg.Ingest.BranchTreesLimit()
	if err != nil {
		return ScanOptions{}, err
	}
	return ScanOptions{
		MaxBlobBytes:     cfg.Ingest.MaxBlobBytes,
		MaxCommits:       cfg.Ingest.MaxCommits,
		MaxCommitAgeDays: cfg.Ingest.MaxCommitAgeDays,
		TagTrees:         cfg.Ingest.TagTrees,
		BranchTrees:      branchTrees,
		BranchTreesAll:   all,
		Archives:         cfg.Ingest.Archives,
		Insights: InsightsOptions{
			Enabled:           cfg.Ingest.Insights.Enabled,
			Samples:           cfg.Ingest.Insights.Samples,
			MaxBytesPerSample: cfg.Ingest.Insights.MaxBytesPerSample,
		},
		HostedMaxFileBytes: cfg.Hosting.MaxFileBytes,
		Contributors:       BuildContributorIndex(cfg.Contributors),
	}, nil
}

// ArchiveData is one produced archive: its artifact-relative path and its bytes.
type ArchiveData struct {
	File string
	Data []byte
}

// ScanResult is one scanned repository, or the reason it was skipped.
type ScanResult struct {
	Repo     *model.Repo
	Blobs    map[string][]byte
	Archives []ArchiveData
	// Skipped is true when AbsPath is not a git repository. Warning then says so, and Repo is
	// nil — a missing path is a warning, never a build failure.
	Skipped bool
	Warning *model.Warning
}

// ScanRepo assembles one repository's artifact record. It never reads the working tree.
func ScanRepo(ctx context.Context, source ScanSource, opts ScanOptions) (ScanResult, error) {
	repoPath := source.AbsPath
	slug, err := SlugFor(repoPath, source.Slug)
	if err != nil {
		return ScanResult{}, err
	}

	isRepo, err := IsGitRepo(ctx, repoPath)
	if err != nil {
		return ScanResult{}, err
	}
	if !isRepo {
		return ScanResult{Skipped: true, Warning: &model.Warning{
			Code:    "repo-not-found",
			Repo:    nil,
			Message: fmt.Sprintf("%s is not a git repository (configured slug '%s'); skipped", repoPath, slug),
		}}, nil
	}

	warnings := []model.Warning{}
	warn := func(w model.Warning) {
		s := slug
		w.Repo = &s
		warnings = append(warnings, w)
	}
	warnAll := func(ws []model.Warning) {
		for _, w := range ws {
			warn(w)
		}
	}

	/* ---- refs ---- */

	branchRefs, err := ListBranchRefs(ctx, repoPath)
	if err != nil {
		return ScanResult{}, err
	}
	def, err := DetectDefaultBranch(ctx, repoPath, branchRefs)
	if err != nil {
		return ScanResult{}, err
	}
	warnAll(def.Warnings)
	empty := def.Name == ""
	if empty {
		warn(model.Warning{Code: "repo-empty", Message: "repository has no commits on any branch"})
	}

	branchesRes, err := LoadBranches(ctx, repoPath, branchRefs, opts.MaxCommits, opts.MaxCommitAgeDays)
	if err != nil {
		return ScanResult{}, err
	}
	warnAll(branchesRes.Warnings)

	gitTags := []model.Tag{}
	if !empty {
		gitTags, err = LoadTags(ctx, repoPath)
		if err != nil {
			return ScanResult{}, err
		}
	}
	commits, err := LoadCommits(ctx, repoPath, shasOf(branchesRes.Shas))
	if err != nil {
		return ScanResult{}, err
	}

	/* ---- hosted branches (schema v7) ---- */

	// Resolve each hosting request against the real branch list. A hosted branch's tree is
	// scanned with the bigger hosted cap — including the default branch when it is the hosted
	// one, which raises that whole branch's stored-file cap.
	branchNames := make([]string, len(branchRefs))
	for i, b := range branchRefs {
		branchNames[i] = b.Name
	}
	hostedBranches := map[string]bool{}
	for _, req := range source.HostedRequests {
		if resolved := ResolveHostedBranch(branchNames, req); resolved != "" {
			hostedBranches[resolved] = true
		}
	}
	hostedCap := opts.MaxBlobBytes
	if opts.HostedMaxFileBytes > hostedCap {
		hostedCap = opts.HostedMaxFileBytes
	}
	capFor := func(refName string) int64 {
		if refName != "" && hostedBranches[refName] {
			return hostedCap
		}
		return opts.MaxBlobBytes
	}

	/* ---- default-branch tree ---- */

	head := ""
	for _, b := range branchRefs {
		if b.Name == def.Name && def.Name != "" {
			head = b.Head
			break
		}
	}
	treeRes := emptyTreeScan()
	if head != "" {
		treeRes, err = ScanTree(ctx, repoPath, head, capFor(def.Name))
		if err != nil {
			return ScanResult{}, err
		}
		if len(treeRes.Tree) == 0 {
			warn(model.Warning{
				Code:    "default-branch-empty-tree",
				Message: fmt.Sprintf("default branch '%s' has no files at HEAD", def.Name),
			})
		}
	}

	/* ---- per-ref trees ---- */

	blobs := map[string][]byte{}
	for sha, buf := range treeRes.Blobs {
		blobs[sha] = buf
	}
	// Keyed by cap AND commit: a hosted branch can share a commit with the default branch or a
	// tag, and the two scans store different file sets (the hosted cap is bigger). The same
	// commit at the same cap still reuses the scan.
	type cachedTree struct {
		tree  []model.TreeEntry
		files map[string]model.FileInfo
	}
	treeCache := map[string]cachedTree{}
	if head != "" {
		treeCache[strconv.FormatInt(capFor(def.Name), 10)+":"+head] = cachedTree{treeRes.Tree, treeRes.Files}
	}
	treeFor := func(commit string, maxBytes int64) (cachedTree, error) {
		key := strconv.FormatInt(maxBytes, 10) + ":" + commit
		if cached, ok := treeCache[key]; ok {
			return cached, nil
		}
		res, err := ScanTree(ctx, repoPath, commit, maxBytes)
		if err != nil {
			return cachedTree{}, err
		}
		for sha, buf := range res.Blobs {
			blobs[sha] = buf
		}
		entry := cachedTree{res.Tree, res.Files}
		treeCache[key] = entry
		return entry, nil
	}

	// Branch trees are capped exactly like tag trees, and for the same reason: tree/blob/raw
	// pages are generated per browsable ref, so N branches multiply the page count by N. The
	// default branch is always browsable and is never counted against the cap; the rest are
	// kept most-recently-updated first, then emitted in name order so refTrees key order stays
	// deterministic.
	nonDefaultBranches := []BranchRef{}
	for _, b := range branchRefs {
		if b.Name != def.Name {
			nonDefaultBranches = append(nonDefaultBranches, b)
		}
	}
	branchesByDateDesc := append([]BranchRef(nil), nonDefaultBranches...)
	sort.SliceStable(branchesByDateDesc, func(i, j int) bool {
		a, b := branchesByDateDesc[i], branchesByDateDesc[j]
		if a.HeadDate != b.HeadDate {
			return a.HeadDate > b.HeadDate
		}
		return a.Name < b.Name
	})
	cappedBranches := branchesByDateDesc
	if !opts.BranchTreesAll && opts.BranchTrees < len(branchesByDateDesc) {
		cappedBranches = branchesByDateDesc[:opts.BranchTrees]
	}
	inCapped := map[string]bool{}
	for _, b := range cappedBranches {
		inCapped[b.Name] = true
	}
	// A hosted branch always gets a tree, cap or no cap (schema v7): a dormant gh-pages is
	// exactly the branch the most-recently-updated cap would drop, and a hosted site with no
	// tree has no bytes to serve.
	treedBranches := append([]BranchRef(nil), cappedBranches...)
	for _, b := range nonDefaultBranches {
		if hostedBranches[b.Name] && !inCapped[b.Name] {
			treedBranches = append(treedBranches, b)
		}
	}
	sort.SliceStable(treedBranches, func(i, j int) bool { return treedBranches[i].Name < treedBranches[j].Name })

	browsableBranchCount := len(treedBranches)
	if def.Name != "" {
		browsableBranchCount++
	}
	if browsableBranchCount < len(branchRefs) {
		branchTreeCap := strconv.Itoa(opts.BranchTrees)
		if opts.BranchTreesAll {
			branchTreeCap = "all"
		}
		warn(model.Warning{
			Code: "branch-trees-capped",
			Message: fmt.Sprintf("%d of %d branches have browsable trees (ingest.branchTrees = %s)",
				browsableBranchCount, len(branchRefs), branchTreeCap),
		})
	}

	refTrees := model.NewRefTreeMap()
	// refTreeOrder preserves the TypeScript's object insertion order — branches by name, then
	// tags by name — which the unservable-path scan below walks. The artifact itself is
	// key-sorted by the JSON encoder, so this order matters only for warning order.
	refTreeOrder := []string{}
	for _, ref := range treedBranches {
		t, err := treeFor(ref.Head, capFor(ref.Name))
		if err != nil {
			return ScanResult{}, err
		}
		refTrees.Set(ref.Name, model.RefTree{Kind: "branch", Name: ref.Name, Commit: ref.Head, Tree: t.tree, Files: t.files})
		refTreeOrder = append(refTreeOrder, ref.Name)
	}

	tagsByDateDesc := append([]model.Tag(nil), gitTags...)
	sort.SliceStable(tagsByDateDesc, func(i, j int) bool {
		a, b := tagsByDateDesc[i], tagsByDateDesc[j]
		if a.Date != b.Date {
			return a.Date > b.Date
		}
		return a.Name < b.Name
	})
	treedTags := tagsByDateDesc
	if opts.TagTrees < len(tagsByDateDesc) {
		treedTags = tagsByDateDesc[:opts.TagTrees]
	}
	treedTags = append([]model.Tag(nil), treedTags...)
	sort.SliceStable(treedTags, func(i, j int) bool { return treedTags[i].Name < treedTags[j].Name })
	if len(gitTags) > opts.TagTrees {
		warn(model.Warning{
			Code: "tag-trees-capped",
			Message: fmt.Sprintf("%d of %d tags have browsable trees (ingest.tagTrees = %d)",
				len(treedTags), len(gitTags), opts.TagTrees),
		})
	}
	for _, tag := range treedTags {
		if _, taken := refTrees.Get(tag.Name); taken {
			continue // a branch of the same name already claimed the key
		}
		t, err := treeFor(tag.Target, opts.MaxBlobBytes)
		if err != nil {
			return ScanResult{}, err
		}
		refTrees.Set(tag.Name, model.RefTree{Kind: "tag", Name: tag.Name, Commit: tag.Target, Tree: t.tree, Files: t.files})
		refTreeOrder = append(refTreeOrder, tag.Name)
	}

	/* ---- zip source archives: default branch + every treed tag ---- */

	archives := []model.Archive{}
	archiveData := []ArchiveData{}
	if opts.Archives {
		type target struct {
			ref, kind, commit string
		}
		targets := []target{}
		if head != "" && def.Name != "" {
			targets = append(targets, target{def.Name, "branch", head})
		}
		for _, tag := range treedTags {
			targets = append(targets, target{tag.Name, "tag", tag.Target})
		}
		for _, t := range targets {
			file := "archives/" + slug + "/" + routes.RefSlug(t.ref) + ".zip"
			data, err := GitOutputBytes(ctx, repoPath,
				"archive", "--format=zip", "--prefix="+slug+"-"+routes.RefSlug(t.ref)+"/", t.commit)
			if err != nil {
				return ScanResult{}, err
			}
			archives = append(archives, model.Archive{
				Ref: t.ref, Kind: t.kind, Commit: t.commit, File: file, Bytes: int64(len(data)),
			})
			archiveData = append(archiveData, ArchiveData{File: file, Data: data})
		}
	}

	/* ---- metadata ---- */

	metaFile := RepoMetaResult{Warnings: []model.Warning{}}
	if head != "" {
		metaFile, err = ReadRepoMetaFile(ctx, repoPath, head)
		if err != nil {
			return ScanResult{}, err
		}
	}
	warnAll(metaFile.Warnings)

	defaultName := source.DefaultName
	if defaultName == "" {
		defaultName = RepoBasename(repoPath)
	}
	// Precedence: config overrides > .frznforge.json > provider metadata > derived defaults.
	// The two lower layers are pre-merged so mergeMeta's own two-layer pick still applies.
	meta, err := mergeMeta(defaultName, layerMeta(metaFile.Meta, source.ProviderMeta), source.Overrides)
	if err != nil {
		return ScanResult{}, err
	}
	releaseMode := "tags"
	switch {
	case source.Overrides != nil && source.Overrides.ReleaseMode != nil:
		releaseMode = *source.Overrides.ReleaseMode
	case metaFile.Meta != nil && metaFile.Meta.ReleaseMode != nil:
		releaseMode = *metaFile.Meta.ReleaseMode
	case source.ReleaseMode != "":
		releaseMode = source.ReleaseMode
	}
	releases := []model.Release{}
	if releaseMode == "provider" && source.Releases != nil {
		releases = source.Releases
	}

	/* ---- root-level files: license + readme ---- */

	rootEntries := []RawTreeEntry{}
	if head != "" {
		rootEntries, err = ListRootTree(ctx, repoPath, head)
		if err != nil {
			return ScanResult{}, err
		}
	}
	readContent := func(sha string) ([]byte, error) {
		if buf, ok := treeRes.Contents[sha]; ok {
			return buf, nil
		}
		return ReadBlob(ctx, repoPath, sha)
	}

	var detected *detectedLicense
	if licEntry := findLicenseEntry(rootEntries); licEntry != nil {
		buf, err := readContent(licEntry.Sha)
		if err != nil {
			return ScanResult{}, err
		}
		spdx := ""
		if !LooksBinary(buf) {
			spdx = detectSpdx(string(buf))
		}
		detected = &detectedLicense{File: licEntry.Path, Spdx: spdx}
	}
	license := resolveLicense(detected, meta.License)

	var readme *model.Readme
	if readmeEntry := findReadmeEntry(rootEntries); readmeEntry != nil && sizeOf(*readmeEntry) <= opts.MaxBlobBytes {
		buf, err := readContent(readmeEntry.Sha)
		if err != nil {
			return ScanResult{}, err
		}
		if !LooksBinary(buf) {
			readme = &model.Readme{Path: readmeEntry.Path, Sha: readmeEntry.Sha, Content: string(buf)}
		}
	}

	/* ---- paths a static URL cannot round-trip ---- */

	// A committed path (or ref name) carrying '#' or '%' can be listed but not linked. Warn
	// once per offender so the owner learns why a file has no page, rather than the build
	// aborting on an invalid URL or the site shipping a dead link. Refs are checked on the
	// slugged name, which is what actually goes into the URL.
	type refEntries struct {
		name    string
		entries []model.TreeEntry
	}
	checked := []refEntries{{def.Name, treeRes.Tree}}
	for _, name := range refTreeOrder {
		rt, _ := refTrees.Get(name)
		checked = append(checked, refEntries{rt.Name, rt.Tree})
	}
	for _, ref := range checked {
		if ref.name == "" {
			continue
		}
		if !routes.IsRawServable(routes.RefSlug(ref.name)) {
			warn(model.Warning{
				Code: "repo-path-unservable",
				Message: fmt.Sprintf(
					"ref '%s' contains '#' or '%%', which a static URL cannot round-trip; its files are not browsable",
					ref.name),
			})
			continue
		}
		for _, e := range ref.entries {
			if e.Type != "blob" && e.Type != "symlink" && e.Type != "tree" {
				continue
			}
			if routes.IsRawServable(e.Path) {
				continue
			}
			warn(model.Warning{
				Code: "repo-path-unservable",
				Message: fmt.Sprintf(
					"'%s' on ref '%s' contains '#' or '%%', which a static URL cannot round-trip; "+
						"it is listed in the file table but has no page", e.Path, ref.name),
			})
		}
	}

	/* ---- display-support commits (schema v6) ---- */

	// Per-path lastCommit and tag targets that fall outside the kept history. Kept in a
	// separate map so `commits` — and everything derived from it (counts, contributors, dates,
	// insights, activity) — stays exactly the narrowed branch history, while file tables and
	// tag rows can still name the commit they point at. Two things land here: commits
	// maxCommits/maxCommitAgeDays dropped, and tag targets reachable from no branch at all (a
	// rebase-orphaned release tag) — so this can be non-empty with no limit configured.
	extraShas := map[string]struct{}{}
	noteExtra := func(sha string) {
		if sha == "" {
			return
		}
		if _, kept := branchesRes.Shas[sha]; !kept {
			extraShas[sha] = struct{}{}
		}
	}
	for _, e := range treeRes.Tree {
		noteExtra(e.LastCommit)
	}
	for _, name := range refTreeOrder {
		for _, e := range refTreeOf(refTrees, name).Tree {
			noteExtra(e.LastCommit)
		}
	}
	for _, t := range gitTags {
		noteExtra(t.Target)
	}
	extraCommits, err := LoadCommits(ctx, repoPath, shasOf(extraShas))
	if err != nil {
		return ScanResult{}, err
	}

	/* ---- dates ---- */

	var createdAt, updatedAt *string
	for _, sha := range sortedCommitShas(commits) {
		date := commits[sha].CommitDate
		if createdAt == nil || date < *createdAt {
			d := date
			createdAt = &d
		}
		if updatedAt == nil || date > *updatedAt {
			d := date
			updatedAt = &d
		}
	}

	/* ---- insights (schema v5) ---- */

	var defaultBranchCommits []string
	if def.Name != "" {
		for _, b := range branchesRes.Branches {
			if b.Name == def.Name {
				defaultBranchCommits = b.Commits
				break
			}
		}
	}
	insightsRes, err := ComputeInsights(ctx, repoPath, ComputeInsightsArgs{
		Commits:       commits,
		BranchCommits: defaultBranchCommits,
		Head:          head,
		Options:       opts.Insights,
	})
	if err != nil {
		return ScanResult{}, err
	}
	warnAll(insightsRes.Warnings)

	repoSource := model.RepoSource{Type: "local", Path: &repoPath}
	if source.Source != nil {
		repoSource = *source.Source
	}
	var defaultBranch *string
	if def.Name != "" {
		name := def.Name
		defaultBranch = &name
	}

	repo := &model.Repo{
		Slug:          slug,
		Name:          meta.Name,
		Description:   meta.Description,
		Source:        repoSource,
		Links:         meta.Links,
		Tags:          meta.Tags,
		Template:      meta.Template,
		License:       license,
		ReleaseMode:   releaseMode,
		Releases:      releases,
		Empty:         empty,
		DefaultBranch: defaultBranch,
		Branches:      branchesRes.Branches,
		GitTags:       gitTags,
		Commits:       commits,
		CommitCount:   int64(len(commits)),
		ExtraCommits:  extraCommits,
		Tree:          treeRes.Tree,
		Files:         treeRes.Files,
		RefTrees:      *refTrees,
		Archives:      archives,
		Languages:     LanguageStats(treeRes.Files),
		Contributors:  ContributorsFromCommits(commits, opts.Contributors),
		Insights:      insightsRes.Insights,
		Readme:        readme,
		CreatedAt:     createdAt,
		UpdatedAt:     updatedAt,
		Warnings:      warnings,
	}
	return ScanResult{Repo: repo, Blobs: blobs, Archives: archiveData}, nil
}

// layerMeta merges two metadata layers field by field, `high` winning wherever it is set.
func layerMeta(high, low *config.RepoMetaInput) *config.RepoMetaInput {
	if low == nil {
		return high
	}
	if high == nil {
		return low
	}
	merged := *low
	if high.Name != nil {
		merged.Name = high.Name
	}
	if high.Description != nil {
		merged.Description = high.Description
	}
	if high.Tags != nil {
		merged.Tags = high.Tags
	}
	if high.Template != nil {
		merged.Template = high.Template
	}
	if high.License != nil {
		merged.License = high.License
	}
	if high.ReleaseMode != nil {
		merged.ReleaseMode = high.ReleaseMode
	}
	// links merge per key rather than being replaced wholesale, matching mergeMeta: a provider
	// homepage must survive a repo file that only sets `issues`.
	links := config.RepoLinks{}
	pick := func(get func(*config.RepoLinks) *string) *string {
		if high.Links != nil {
			if v := get(high.Links); v != nil {
				return v
			}
		}
		if low.Links != nil {
			return get(low.Links)
		}
		return nil
	}
	links.Homepage = pick(func(l *config.RepoLinks) *string { return l.Homepage })
	links.Issues = pick(func(l *config.RepoLinks) *string { return l.Issues })
	links.Donations = pick(func(l *config.RepoLinks) *string { return l.Donations })
	links.Upstream = pick(func(l *config.RepoLinks) *string { return l.Upstream })
	if links.Homepage != nil || links.Issues != nil || links.Donations != nil || links.Upstream != nil {
		merged.Links = &links
	}
	return &merged
}

func sortedCommitShas(commits map[string]model.Commit) []string {
	out := make([]string, 0, len(commits))
	for sha := range commits {
		out = append(out, sha)
	}
	sort.Strings(out)
	return out
}

// refTreeOf is a small reader for the ordered ref-tree map, so call sites that only want the
// tree do not each have to discard the ok.
func refTreeOf(m *model.RefTreeMap, name string) model.RefTree {
	t, _ := m.Get(name)
	return t
}
