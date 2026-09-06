package ingest

import (
	"context"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"frznforge/internal/model"
)

// RawTreeEntry is one `ls-tree` row before it becomes a model.TreeEntry.
type RawTreeEntry struct {
	Mode string
	// Type is blob, tree, commit (submodule) or symlink.
	Type string
	Sha  string
	// Size is nil for trees and submodules, and for a blob whose size git reported as "-".
	Size *int64
	Path string
	Name string
}

// lsTreeRe matches the part of an `ls-tree -l` row before the tab: mode, type, object id and
// a right-aligned size (hence the run of spaces).
var lsTreeRe = regexp.MustCompile(`^(\d+) (\w+) ([0-9a-f]{40}) +(\S+)$`)

// parseLsTree turns `-z` ls-tree output into entries, sorted by path.
//
// -z is what keeps this honest on paths: without it git applies core.quotepath and hands back
// C-style escaped, double-quoted names for anything non-ASCII, which would have to be
// un-escaped byte for byte. With -z the path is the literal committed bytes, and the record
// separator cannot occur inside one.
func parseLsTree(out []byte) []RawTreeEntry {
	entries := []RawTreeEntry{}
	for _, rec := range SplitNUL(out) {
		tab := strings.IndexByte(rec, '\t')
		if tab == -1 {
			continue
		}
		m := lsTreeRe.FindStringSubmatch(rec[:tab])
		if m == nil {
			continue
		}
		mode, gitType, sha, sizeStr := m[1], m[2], m[3], m[4]
		path := rec[tab+1:]

		var entryType string
		switch {
		case mode == "160000" || gitType == "commit":
			entryType = "commit"
		case mode == "120000":
			entryType = "symlink"
		case gitType == "tree":
			entryType = "tree"
		default:
			entryType = "blob"
		}
		var size *int64
		if entryType != "tree" && entryType != "commit" && sizeStr != "-" {
			if n, err := strconv.ParseInt(sizeStr, 10, 64); err == nil {
				size = &n
			}
		}
		name := path
		if slash := strings.LastIndexByte(path, '/'); slash != -1 {
			name = path[slash+1:]
		}
		entries = append(entries, RawTreeEntry{Mode: mode, Type: entryType, Sha: sha, Size: size, Path: path, Name: name})
	}
	sort.SliceStable(entries, func(i, j int) bool { return entries[i].Path < entries[j].Path })
	return entries
}

// ListTree lists every entry at every depth of a treeish, trees included, sorted by path.
func ListTree(ctx context.Context, repo, treeish string) ([]RawTreeEntry, error) {
	out, err := GitOutputBytes(ctx, repo, "ls-tree", "-r", "-l", "-t", "-z", treeish, "--")
	if err != nil {
		return nil, err
	}
	return parseLsTree(out), nil
}

// ListRootTree lists the root-level entries of a treeish only, sorted by path.
func ListRootTree(ctx context.Context, repo, treeish string) ([]RawTreeEntry, error) {
	out, err := GitOutputBytes(ctx, repo, "ls-tree", "-l", "-z", treeish, "--")
	if err != nil {
		return nil, err
	}
	return parseLsTree(out), nil
}

// LastCommitByPath maps each wanted path to the newest commit (walking rev newest-first) that
// touched it. A directory takes the newest of its descendants; paths never seen are absent,
// and the caller substitutes the head commit.
//
// The walk stops as soon as every wanted path has an answer, which is what keeps this one
// `git log` rather than one per file.
func LastCommitByPath(ctx context.Context, repo, rev string, paths []string) (map[string]string, error) {
	wanted := make(map[string]struct{}, len(paths))
	for _, p := range paths {
		wanted[p] = struct{}{}
	}
	result := make(map[string]string, len(wanted))
	if len(wanted) == 0 {
		return result, nil
	}
	out, err := GitOutput(ctx, repo, "log", "--format=%x1e%H", "--name-only", "-z", rev, "--")
	if err != nil {
		return nil, err
	}
	remaining := len(wanted)
	for _, chunk := range strings.Split(out, recordSep) {
		if chunk == "" {
			continue
		}
		nul := strings.IndexByte(chunk, 0)
		shaPart := chunk
		if nul != -1 {
			shaPart = chunk[:nul]
		}
		sha := jsTrim(shaPart)
		if !shaLineRe.MatchString(sha) || nul == -1 {
			continue
		}
		for _, raw := range strings.Split(chunk[nul+1:], fieldSep) {
			p := strings.Trim(raw, "\n")
			if p == "" {
				continue
			}
			// the path itself and every ancestor directory
			cur := p
			for {
				if _, want := wanted[cur]; want {
					if _, have := result[cur]; !have {
						result[cur] = sha
						remaining--
					}
				}
				i := strings.LastIndexByte(cur, '/')
				if i == -1 {
					break
				}
				cur = cur[:i]
			}
		}
		if remaining <= 0 {
			break
		}
	}
	return result, nil
}

// TreeScanResult is one ref's browsable tree: entries, per-file info, and the blob bytes the
// artifact will store.
type TreeScanResult struct {
	Tree  []model.TreeEntry
	Files map[string]model.FileInfo
	// Blobs is sha → content for files that are actually stored.
	Blobs map[string][]byte
	// Contents is sha → content for every blob that was read, so the readme and metadata
	// lookups can reuse bytes already in memory instead of shelling out again.
	Contents map[string][]byte
}

// emptyTreeScan is what an empty repository's default branch scans to.
func emptyTreeScan() TreeScanResult {
	return TreeScanResult{
		Tree:     []model.TreeEntry{},
		Files:    map[string]model.FileInfo{},
		Blobs:    map[string][]byte{},
		Contents: map[string][]byte{},
	}
}

// ScanTree scans the tree of a commit: entries with their last-touching commit, per-file
// binary/size classification, and the contents of every stored blob.
func ScanTree(ctx context.Context, repo, head string, maxBlobBytes int64) (TreeScanResult, error) {
	raw, err := ListTree(ctx, repo, head)
	if err != nil {
		return TreeScanResult{}, err
	}
	paths := make([]string, len(raw))
	for i, e := range raw {
		paths[i] = e.Path
	}
	last, err := LastCommitByPath(ctx, repo, head, paths)
	if err != nil {
		return TreeScanResult{}, err
	}

	tree := make([]model.TreeEntry, 0, len(raw))
	for _, e := range raw {
		lastCommit, ok := last[e.Path]
		if !ok {
			lastCommit = head
		}
		tree = append(tree, model.TreeEntry{
			Path: e.Path, Name: e.Name, Type: e.Type, Mode: e.Mode,
			Sha: e.Sha, Size: e.Size, LastCommit: lastCommit,
		})
	}

	fileEntries := make([]RawTreeEntry, 0, len(raw))
	for _, e := range raw {
		if e.Type == "blob" || e.Type == "symlink" {
			fileEntries = append(fileEntries, e)
		}
	}
	smallShas := []string{}
	for _, e := range fileEntries {
		if sizeOf(e) <= maxBlobBytes {
			smallShas = append(smallShas, e.Sha)
		}
	}
	contents, err := ReadBlobs(ctx, repo, smallShas)
	if err != nil {
		return TreeScanResult{}, err
	}

	// Binary sniff for oversized blobs: only the first 8000 bytes are read, which is all
	// git's own heuristic looks at.
	bigBinary := map[string]bool{}
	for _, e := range fileEntries {
		if sizeOf(e) <= maxBlobBytes {
			continue
		}
		if _, done := bigBinary[e.Sha]; done {
			continue
		}
		prefix, err := ReadBlobPrefix(ctx, repo, e.Sha, 8000)
		if err != nil {
			return TreeScanResult{}, err
		}
		bigBinary[e.Sha] = LooksBinary(prefix)
	}

	files := make(map[string]model.FileInfo, len(fileEntries))
	blobs := map[string][]byte{}
	for _, e := range fileEntries {
		size := sizeOf(e)
		tooLarge := size > maxBlobBytes
		content, haveContent := contents[e.Sha]
		binary := false
		switch {
		case tooLarge:
			binary = bigBinary[e.Sha]
		case haveContent:
			binary = LooksBinary(content)
		}
		// schema v2: any file within the size cap is stored, binary included, because the raw
		// route and the preview both serve bytes.
		stored := !tooLarge && haveContent
		files[e.Path] = model.FileInfo{
			Path: e.Path, Sha: e.Sha, Size: size, Binary: binary,
			TooLarge: tooLarge, Stored: stored, Language: DetectLanguage(e.Path),
		}
		if stored {
			blobs[e.Sha] = content
		}
	}

	return TreeScanResult{Tree: tree, Files: files, Blobs: blobs, Contents: contents}, nil
}

func sizeOf(e RawTreeEntry) int64 {
	if e.Size == nil {
		return 0
	}
	return *e.Size
}
