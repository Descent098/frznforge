---
title: Git plumbing frznforge's ingest actually uses
description: The seven git invocations behind a whole forge site, and the exact shape of what each one prints.
date: 2026-05-14
tags:
  - git
  - frznforge
  - ingest
---

# Git plumbing frznforge's ingest actually uses

The entire site is built from a handful of git calls. No libgit2, no `.git` parsing by hand —
just the CLI, run against a repo path, with `-z` and `%x1e`/`%x1f` separators so nothing has to
be re-quoted. Everything below is what `src/lib/ingest/` really runs.

## The seven calls

| Command | What it answers | Output shape |
| --- | --- | --- |
| `git rev-parse --is-bare-repository` | bare or working copy? | `true` / `false` |
| `git rev-parse --show-toplevel` | where does this repo start? | one absolute path |
| `git for-each-ref refs/tags` | every tag, annotated and lightweight | one record per tag |
| `git rev-list --topo-order <head>` | commit list for a branch | one sha per line, newest first |
| `git log --no-walk=unsorted --stdin` | commit metadata for a sha set | one record per sha, input order |
| `git ls-tree -r -l -t -z <treeish>` | the file tree at a ref | mode, type, sha, size, path |
| `git cat-file --batch` | blob bytes, many at a time | header line, then raw bytes |

## Two habits that make parsing boring

**Pick separators the data cannot contain.** ASCII record/unit separators are the trick:

```sh
git log --no-walk=unsorted --stdin -z \
  --format='%H%x1f%P%x1f%an%x1f%ae%x1f%aI%x1f%cn%x1f%ce%x1f%cI%x1f%s%x1f%b' --
```

`%x1f` between fields, `-z` between records. A commit body can hold newlines, quotes and
emoji; it will not hold a `0x1f`. Note the body comes last on purpose — if it somehow *does*
contain the separator, you rejoin the tail instead of losing fields.

**Batch instead of looping.** `--stdin` and `cat-file --batch` both take a sha list on stdin,
so a thousand commits or a thousand blobs cost one process, not a thousand. On Windows that is
the difference between a build and a coffee break.

## Things that bit me

- `%(objectname)` on an annotated tag is the *tag object*, not the commit. You want
  `%(*objectname)`, and you have to handle a tag that points at a tree or a blob.
- Merge commits emit no `--numstat` records under plain `git log`. Empty file list, not a bug.
- `git rev-list` has no natural bound. Cap it (`--max-count`) and warn when you truncate,
  otherwise one repo with 400k commits decides how big your JSON is.
- Dates: ask for `%aI` / `iso-strict` and normalise to UTC once, at the boundary. Mixing
  offsets into a JSON artifact makes diffs unreadable.

Reference for the format placeholders:
[git-log pretty formats](https://git-scm.com/docs/pretty-formats).
