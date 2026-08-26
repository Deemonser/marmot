// Package memtree holds a scan result in memory and nothing else.
//
// There is no file, no manifest, no index and no cache (ADR-0055). A result
// lives exactly as long as the process that produced it; when the app starts
// there is nothing to reopen, so the only way forward is to scan again. That is
// the point: the product is a disk cleaner, and a full-disk snapshot cost 843 MB
// on disk while the in-memory form of the same tree costs less (R-057).
//
// A consequence that must not be dressed up: a crash loses the result outright.
package memtree

import "errors"

var (
	// ErrInvalidRequest covers malformed queries: unknown snapshot, negative
	// offset, a page limit past the cap.
	ErrInvalidRequest = errors.New("invalid snapshot request")
	// ErrResultUnavailable means the tree exists but has not been finished yet, so
	// there is nothing consistent to answer with.
	ErrResultUnavailable = errors.New("scan result is unavailable")
	ErrNodeNotFound      = errors.New("node not found")
)

const (
	// maxPageSize caps a single Children/Map page. The projection budget is
	// bounded separately (ADR-0048).
	maxPageSize = 1000
)
