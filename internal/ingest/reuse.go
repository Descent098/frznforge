package ingest

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"frznforge/internal/model"
)

// Port of src/lib/ingest/reuse.ts: cross-run reuse of ingest work (ingest.reuse).
//
// Two mechanisms, both living entirely in ingest.cacheDir sidecars so forge.json and the
// provider .meta.json keep their documented no-timestamp promises:
//
//   - The RUN LOG (<cacheDir>/last-run.json): when and how well each remote source was last
//     fetched. The freshness window reads it to skip re-fetching a source whose last fetch
//     succeeded moments ago; a degraded fetch (failed, rate-limited, served stale) is never
//     window-skipped, so a rate-limited refresh heals itself run by run.
//   - The SCAN CACHE (<cacheDir>/scan/<digest>.json): one repo's complete ScanRepo output, keyed
//     by a digest over everything the scan reads — every branch and tag ref (names + object ids),
//     HEAD, the scan source (slug, overrides, provider metadata layer, releases, artifact
//     `source` value) and the ScanOptions. Provider metadata is part of the key on purpose:
//     descriptions, links and releases change with no ref-head change, and a key without them
//     would replay stale releases into a schema-valid artifact.
//
// Reuse must never change artifact bytes: a hit replays the recorded result — validated, with
// blob and archive bytes read back from the content-addressed stores in outDir — or, when
// anything is missing or invalid, quietly falls back to a real scan. Timestamps are confined to
// the run log; clocks are injected (PrepareRemoteDeps.Now) so tests stay deterministic.

/* ---- shared -------------------------------------------------------------- */

// StableStringify is JSON with recursively sorted object keys, so a digest does not depend on
// field order. Go's encoder already sorts map keys, so the value is round-tripped through a
// generic map to turn every struct into one.
//
// It is a DIGEST INPUT only. The bytes differ from the TypeScript's stableStringify (Go and
// JavaScript spell a struct's fields differently), which is why a scan-cache entry written by
// one implementation is simply never a hit for the other: the digest will not match, the real
// scan runs, and the entry is rewritten. That is the safe direction — a cache miss costs time,
// never correctness.
func StableStringify(value any) (string, error) {
	raw, err := json.Marshal(value)
	if err != nil {
		return "", err
	}
	var generic any
	dec := json.NewDecoder(bytes.NewReader(raw))
	// Numbers keep their original spelling, so a digest cannot shift with float formatting.
	dec.UseNumber()
	if err := dec.Decode(&generic); err != nil {
		return "", err
	}
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(generic); err != nil {
		return "", err
	}
	return strings.TrimSuffix(buf.String(), "\n"), nil
}

func sha256Hex(text string) string {
	sum := sha256.Sum256([]byte(text))
	return hex.EncodeToString(sum[:])
}

// ConfigHashFor is the hash of the resolved config; a mismatch invalidates the whole run log.
func ConfigHashFor(cfg any) (string, error) {
	encoded, err := StableStringify(cfg)
	if err != nil {
		return "", err
	}
	return sha256Hex(encoded), nil
}

// readJSONFile decodes a JSON sidecar; nil means "nothing recorded" — absent, unreadable and
// corrupt all mean the same thing here.
func readJSONFile(file string, out any) bool {
	raw, err := os.ReadFile(file)
	if err != nil {
		return false
	}
	return json.Unmarshal(raw, out) == nil
}

// writeJSONFile is a best-effort write: a read-only or full cache directory must not fail a
// build.
func writeJSONFile(file string, value any) {
	encoded, err := marshalJSONLikeStringify(value)
	if err != nil {
		return
	}
	if os.MkdirAll(filepath.Dir(file), 0o755) != nil {
		return
	}
	_ = os.WriteFile(file, encoded, 0o644)
}

/* ---- run log (freshness window) ------------------------------------------ */

const (
	runLogVersion  = 2
	runLogFilename = "last-run.json"
)

// RunLogEntry is one remote source's record of the previous run.
type RunLogEntry struct {
	// FetchedAt is the wall-clock instant of the last real fetch attempt (never window-skipped
	// runs).
	FetchedAt string `json:"fetchedAt"`
	// Fresh is true when that fetch was fully fresh: mirror updated, no remote-* warnings.
	Fresh bool `json:"fresh"`
	// GitOk and MetaOk are the two halves of a remote fetch, recorded separately (v2). Fresh is
	// the AND of them plus "the repo was not skipped", and keeps its exact 0.2.0 meaning so the
	// freshness window is unaffected; these say WHICH half failed, which is what the cooldown
	// needs ("since the last SUCCESSFUL fetch") and what a build report can show.
	//
	// They are reported by PrepareRemote, not inferred from warning codes, because
	// remote-cache-stale is raised for a stale mirror AND for stale provider metadata — the code
	// alone cannot tell the halves apart.
	GitOk  bool `json:"gitOk"`
	MetaOk bool `json:"metaOk"`
	// Heads are the mirror's refs (refs/heads/* and refs/tags/* → object id) as they stood at the
	// end of this run, or nil when they could not be read. This is the baseline the
	// same-commit-hash skip compares a `git ls-remote` against: all refs equal means the mirror
	// is already current and `git remote update` has nothing to do.
	Heads map[string]string `json:"heads"`
}

// RunLog is the whole sidecar.
type RunLog struct {
	Version int `json:"version"`
	// ConfigHash is ConfigHashFor of the config that produced the entries.
	ConfigHash string `json:"configHash"`
	// Remotes is keyed by the source's mirror path (ResolvedSource.AbsPath) — machine-local.
	Remotes map[string]RunLogEntry `json:"remotes"`
}

// RunLogPathFor is where the run log lives for a given cache directory.
func RunLogPathFor(cacheDir string) string {
	return filepath.Join(cacheDir, runLogFilename)
}

// ReadRunLog reads the run log, or nil when there is nothing usable.
//
// A log written by an older version is discarded wholesale rather than migrated: it is a
// rebuildable cache by definition, and the cost of discarding it is one un-skipped fetch cycle.
func ReadRunLog(cacheDir string) *RunLog {
	var raw struct {
		Version    *int                   `json:"version"`
		ConfigHash *string                `json:"configHash"`
		Remotes    map[string]RunLogEntry `json:"remotes"`
	}
	if !readJSONFile(RunLogPathFor(cacheDir), &raw) {
		return nil
	}
	if raw.Version == nil || *raw.Version != runLogVersion {
		return nil
	}
	if raw.ConfigHash == nil || raw.Remotes == nil {
		return nil
	}
	return &RunLog{Version: runLogVersion, ConfigHash: *raw.ConfigHash, Remotes: raw.Remotes}
}

// WriteRunLog records this run for the next one's freshness window.
func WriteRunLog(cacheDir, configHash string, remotes map[string]RunLogEntry) {
	if remotes == nil {
		remotes = map[string]RunLogEntry{}
	}
	writeJSONFile(RunLogPathFor(cacheDir), RunLog{
		Version:    runLogVersion,
		ConfigHash: configHash,
		Remotes:    remotes,
	})
}

// ReadRefHeads reads the mirror's branch and tag refs as {"refs/heads/main": "<sha>", …}, or nil
// when the path is not a git repository or git failed. Used to stamp RunLogEntry.Heads and, on
// the next run, to compare against `git ls-remote` before paying for a fetch.
func ReadRefHeads(ctx context.Context, absPath string) map[string]string {
	isRepo, err := IsGitRepo(ctx, absPath)
	if err != nil || !isRepo {
		return nil
	}
	// %00 stays git's own escape rather than a literal NUL: an argv entry cannot contain a NUL
	// byte, and passing one fails the exec outright on Windows.
	out, err := GitOutput(ctx, absPath,
		"for-each-ref", "--format=%(refname)"+fieldSepFormat+"%(objectname)", "refs/heads", "refs/tags")
	if err != nil {
		return nil
	}
	return ParseRefLines(out)
}

// ParseRefLines parses for-each-ref / ls-remote style output into {ref: sha}.
//
// ls-remote emits `<sha>\t<ref>`; the for-each-ref call above emits `<ref>\0<sha>`. Both are
// normalised so the two sides of the comparison are directly comparable.
func ParseRefLines(text string) map[string]string {
	refs := map[string]string{}
	for _, line := range splitLines(text) {
		if jsTrim(line) == "" {
			continue
		}
		var ref, sha string
		if strings.Contains(line, "\x00") {
			parts := strings.SplitN(line, "\x00", 2)
			ref, sha = parts[0], parts[1]
		} else {
			fields := splitJSWhitespace(line)
			if len(fields) > 0 {
				sha = fields[0]
			}
			if len(fields) > 1 {
				ref = fields[1]
			}
		}
		if ref == "" || sha == "" {
			continue
		}
		// `^{}` entries are the peeled targets of annotated tags; the tag object id is what both
		// sides already agree on, so keeping them would only add noise.
		if strings.HasSuffix(ref, "^{}") {
			continue
		}
		refs[ref] = sha
	}
	return refs
}

// RefsEqual reports whether two ref maps name exactly the same refs at exactly the same object
// ids.
func RefsEqual(a, b map[string]string) bool {
	if len(a) != len(b) {
		return false
	}
	for k, v := range a {
		if b[k] != v {
			return false
		}
	}
	return true
}

// WithinFreshWindow reports whether entry records a fully fresh fetch inside the window ending
// at now. A nil entry is never in the window.
func WithinFreshWindow(entry *RunLogEntry, now time.Time, maxAgeMinutes float64) bool {
	if entry == nil || !entry.Fresh {
		return false
	}
	at, ok := parseRunLogTime(entry.FetchedAt)
	if !ok {
		return false
	}
	age := now.Sub(at)
	return age >= 0 && age <= time.Duration(maxAgeMinutes*float64(time.Minute))
}

// WithinCooldown reports whether entry records a FULLY SUCCESSFUL fetch (both halves) inside a
// cooldown of `seconds` ending at now. A nil `seconds` disables it.
//
// Deliberately stricter than WithinFreshWindow: a repo whose provider metadata was rate-limited
// must never be held back by the cooldown, or a long cooldown would freeze the gap in place for
// hours. That is the same self-healing rule the freshness window follows, and it composes with
// the misses-first ordering — the repos in trouble go first and are always retried.
func WithinCooldown(entry *RunLogEntry, now time.Time, seconds *int64) bool {
	if seconds == nil || entry == nil {
		return false
	}
	if !entry.GitOk || !entry.MetaOk {
		return false
	}
	at, ok := parseRunLogTime(entry.FetchedAt)
	if !ok {
		return false
	}
	age := now.Sub(at)
	return age >= 0 && age <= time.Duration(*seconds)*time.Second
}

// parseRunLogTime reads a stamp the run log wrote. The run log is the one place a wall clock is
// allowed, and it is written with the same ISO spelling by both implementations.
func parseRunLogTime(value string) (time.Time, bool) {
	for _, layout := range []string{time.RFC3339, "2006-01-02T15:04:05Z0700"} {
		if t, err := time.Parse(layout, value); err == nil {
			return t, true
		}
	}
	return time.Time{}, false
}

// FormatRunLogTime spells an instant the way the run log stores it (ISO 8601 UTC with
// milliseconds, matching Date#toISOString), so the two implementations read each other's stamps.
func FormatRunLogTime(t time.Time) string {
	return t.UTC().Format("2006-01-02T15:04:05.000Z")
}

/* ---- scan cache ---------------------------------------------------------- */

const (
	scanCacheVersion = 1
	scanCacheDirname = "scan"
	// blobDirname mirrors BLOB_DIRNAME in src/lib/data/load.ts.
	blobDirname = "blobs"
)

// ScanCacheArchive is one recorded archive: its outDir-relative path and a hash of its bytes.
type ScanCacheArchive struct {
	File   string `json:"file"`
	Sha256 string `json:"sha256"`
}

// ScanCacheEntry is one repo's recorded scan.
type ScanCacheEntry struct {
	Version int `json:"version"`
	// InputDigest is ScanInputDigest of the inputs that produced this result.
	InputDigest string      `json:"inputDigest"`
	Repo        *model.Repo `json:"repo"`
	// BlobShas are the keys of the blob map at scan time; bytes rehydrate from
	// <outDir>/blobs/<sha>.
	BlobShas []string `json:"blobShas"`
	// Archives are outDir-relative paths plus a content hash; bytes rehydrate from
	// <outDir>/<file> and MUST match the hash. Unlike blobs, archive paths are not
	// content-addressed — they are keyed by slug + ref, and a slug-collision rename means the
	// path recorded here can hold a DIFFERENT repo's zip by the next run. Without the hash check
	// a replay would silently publish the colliding repo's source archive under this repo's URL.
	Archives []ScanCacheArchive `json:"archives"`
}

// ScanCachePathFor is where one repo's scan cache lives, keyed by its absolute path
// (machine-local).
func ScanCachePathFor(cacheDir, absPath string) string {
	return filepath.Join(cacheDir, scanCacheDirname, sha256Hex(absPath)[:16]+".json")
}

// scanDigestInput is the shape hashed by ScanInputDigest, in the same order as the TypeScript's
// object literal (the sort in StableStringify makes the order irrelevant, but keeping it makes
// the two readable side by side).
type scanDigestInput struct {
	Version int         `json:"version"`
	Head    *string     `json:"head"`
	Refs    string      `json:"refs"`
	Source  ScanSource  `json:"source"`
	Opts    ScanOptions `json:"opts"`
}

// ScanInputDigest is a digest over everything ScanRepo reads: HEAD, every branch/tag ref with its
// object id, the scan source and the options. It returns "" when absPath is not a git repository
// — the caller then runs the real scan, which emits the repo-not-found skip itself.
func ScanInputDigest(ctx context.Context, source ScanSource, opts ScanOptions) (string, error) {
	// Every git problem here — not a repository, git not runnable, a listing that failed —
	// answers "no digest", which sends the caller down the real-scan path. That path reports the
	// problem properly; a digest error would only make every caller handle it twice.
	if isRepo, err := IsGitRepo(ctx, source.AbsPath); err != nil || !isRepo {
		return "", nil
	}
	refs, err := GitOutput(ctx, source.AbsPath,
		"for-each-ref", "--format=%(refname)"+fieldSepFormat+"%(objectname)", "refs/heads", "refs/tags")
	if err != nil {
		return "", nil
	}
	var head *string
	if out, ok, err := GitMaybe(ctx, source.AbsPath, "symbolic-ref", "--quiet", "HEAD"); err != nil {
		return "", nil
	} else if ok {
		trimmed := jsTrim(out)
		head = &trimmed
	}
	encoded, err := StableStringify(scanDigestInput{
		Version: scanCacheVersion,
		Head:    head,
		Refs:    refs,
		Source:  source,
		Opts:    opts,
	})
	if err != nil {
		return "", err
	}
	return sha256Hex(encoded), nil
}

// ReadScanCache reads a recorded scan, or nil when there is nothing replayable.
func ReadScanCache(file string) *ScanCacheEntry {
	var raw struct {
		Version     *int               `json:"version"`
		InputDigest *string            `json:"inputDigest"`
		Repo        json.RawMessage    `json:"repo"`
		BlobShas    []string           `json:"blobShas"`
		Archives    []ScanCacheArchive `json:"archives"`
	}
	if !readJSONFile(file, &raw) {
		return nil
	}
	if raw.Version == nil || *raw.Version != scanCacheVersion || raw.InputDigest == nil {
		return nil
	}
	if raw.BlobShas == nil || raw.Archives == nil {
		return nil
	}
	for _, a := range raw.Archives {
		if a.File == "" || a.Sha256 == "" {
			return nil
		}
	}
	// A corrupt cached repo must degrade to a re-scan, never to a validation failure out of the
	// artifact writer that fails the whole build.
	var repo model.Repo
	if json.Unmarshal(raw.Repo, &repo) != nil {
		return nil
	}
	if !repoIsReplayable(&repo) {
		return nil
	}
	return &ScanCacheEntry{
		Version:     scanCacheVersion,
		InputDigest: *raw.InputDigest,
		Repo:        &repo,
		BlobShas:    raw.BlobShas,
		Archives:    raw.Archives,
	}
}

// repoIsReplayable is the Go stand-in for the TypeScript's `Repo.safeParse`: it runs the
// artifact's own validation, and additionally rejects a record whose containers decoded as nil.
//
// The nil check is not paranoia. encoding/json leaves a missing array or object key as a nil
// slice/map, and a nil slice marshals as `null` where the artifact says `[]` — replaying such a
// record would write different bytes for the same commits. The zod schema rejects a missing
// array key for the same reason, so this is the same decision, not a stricter one.
func repoIsReplayable(repo *model.Repo) bool {
	if repo.Tags == nil || repo.Releases == nil || repo.Branches == nil || repo.GitTags == nil ||
		repo.Commits == nil || repo.ExtraCommits == nil || repo.Tree == nil || repo.Files == nil ||
		repo.RefTrees.Len() < 0 || repo.Archives == nil || repo.Languages == nil ||
		repo.Contributors == nil || repo.Warnings == nil {
		return false
	}
	for _, c := range repo.Commits {
		if c.Parents == nil || c.Files == nil {
			return false
		}
	}
	for _, c := range repo.ExtraCommits {
		if c.Parents == nil || c.Files == nil {
			return false
		}
	}
	for _, b := range repo.Branches {
		if b.Commits == nil {
			return false
		}
	}
	for _, t := range repo.RefTrees.All() {
		if t.Tree == nil || t.Files == nil {
			return false
		}
	}
	for _, r := range repo.Releases {
		if r.Assets == nil {
			return false
		}
	}
	if repo.Insights != nil && (repo.Insights.Commits == nil || repo.Insights.CodeSize == nil) {
		return false
	}
	data := model.ForgeData{
		SchemaVersion: model.SchemaVersion,
		Repos:         []model.Repo{*repo},
		Notes:         []model.Note{},
		Organizations: []model.Organization{},
		Hosting:       []model.HostedSite{},
		Warnings:      []model.Warning{},
	}
	return model.Validate(&data) == nil
}

// WriteScanCache records a fresh scan so the next run can replay it.
//
// A skipped scan (no Repo) records nothing: there is no result to replay, and an entry holding a
// null repo would only be read back and rejected.
func WriteScanCache(file, inputDigest string, result ScanResult) {
	if result.Repo == nil {
		return
	}
	shas := make([]string, 0, len(result.Blobs))
	for sha := range result.Blobs {
		shas = append(shas, sha)
	}
	// Go map iteration is randomised; the recorded list is sorted so two runs of the same scan
	// write the same file. Order does not affect a replay (the shas are looked up individually),
	// but a cache file that churns on every run is a cache file nobody can diff.
	sort.Strings(shas)

	archives := make([]ScanCacheArchive, 0, len(result.Archives))
	for _, a := range result.Archives {
		sum := sha256.Sum256(a.Data)
		archives = append(archives, ScanCacheArchive{File: a.File, Sha256: hex.EncodeToString(sum[:])})
	}
	writeJSONFile(file, ScanCacheEntry{
		Version:     scanCacheVersion,
		InputDigest: inputDigest,
		Repo:        result.Repo,
		BlobShas:    shas,
		Archives:    archives,
	})
}

// RehydrateScan replays a cached scan: bytes come back from the content-addressed stores under
// outDir.
//
// It reports ok=false — meaning "re-scan" — when any referenced blob or archive is missing
// (someone deleted data/, or the last run wrote with a different outDir). This is what keeps a
// hit safe against the artifact writer's mirror-and-prune pass: a replay always contributes its
// full buffer maps, so the prune never deletes a cached repo's files.
func RehydrateScan(entry *ScanCacheEntry, outDir string) (ScanResult, bool) {
	blobs := map[string][]byte{}
	for _, sha := range entry.BlobShas {
		data, err := os.ReadFile(filepath.Join(outDir, blobDirname, sha))
		if err != nil {
			return ScanResult{}, false
		}
		blobs[sha] = data
	}
	archives := make([]ArchiveData, 0, len(entry.Archives))
	for _, a := range entry.Archives {
		data, err := os.ReadFile(filepath.Join(outDir, filepath.FromSlash(a.File)))
		if err != nil {
			return ScanResult{}, false
		}
		// Archive paths are slug-keyed, not content-addressed: after a slug-collision rename this
		// path can hold the colliding repo's zip. Wrong content → re-scan, never replay.
		sum := sha256.Sum256(data)
		if hex.EncodeToString(sum[:]) != a.Sha256 {
			return ScanResult{}, false
		}
		archives = append(archives, ArchiveData{File: a.File, Data: data})
	}
	repo := *entry.Repo
	return ScanResult{Repo: &repo, Blobs: blobs, Archives: archives}, true
}

/* ---- CLI ------------------------------------------------------------------ */

// IngestArgs are the flags `frznforge ingest` takes.
type IngestArgs struct {
	// NoCache is --no-cache: ignore the provider cache, the freshness window and the scan cache.
	NoCache bool
	// BackfillMetadata is --backfill-metadata: fetch provider metadata ONLY for repos that have
	// none yet, and touch git for nothing. Everything else replays from cache.
	//
	// For a large account against a spent or anonymous API quota: cloning is unmetered so the
	// commits always arrive, but a normal run re-requests metadata for every repo and runs out of
	// budget before it reaches the ones that never got any — leaving the same tail blank every
	// time. This spends the whole budget on the gaps.
	BackfillMetadata bool
}

// ParseIngestArgs parses the ingest flags, rejecting anything unrecognised.
func ParseIngestArgs(argv []string) (IngestArgs, error) {
	args := IngestArgs{}
	for _, a := range argv {
		switch a {
		case "--no-cache":
			args.NoCache = true
		case "--backfill-metadata":
			args.BackfillMetadata = true
		default:
			return IngestArgs{}, fmt.Errorf(
				"unknown flag: %s (usage: frznforge ingest [--no-cache | --backfill-metadata])", a)
		}
	}
	if args.NoCache && args.BackfillMetadata {
		// --no-cache reads nothing from the provider cache, so every repo would look like a gap
		// and the run would be a full refetch wearing the wrong name.
		return IngestArgs{}, errors.New("--no-cache and --backfill-metadata are opposites; pass only one")
	}
	return args, nil
}
