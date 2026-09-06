//go:build !windows

package ingest

// isWindows drives the one path comparison that has to be case-insensitive; see sameDir.
const isWindows = false
