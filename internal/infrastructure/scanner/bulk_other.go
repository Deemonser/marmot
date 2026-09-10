//go:build !darwin

package scanner

import "errors"

func readDirectoryBulk(string) ([]directoryEntry, error) {
	return nil, errors.New("getattrlistbulk is unavailable on this platform")
}

func readDirectoryBulkFD(int) ([]directoryEntry, error) {
	return nil, errors.New("getattrlistbulk is unavailable on this platform")
}

func openDirectoryPath(string) (int, error) {
	return -1, errors.New("directory file descriptors are unavailable on this platform")
}

func openDirectoryAt(int, string) (int, error) {
	return -1, errors.New("directory file descriptors are unavailable on this platform")
}

func closeDirectoryFD(int) {}

// CloneFactsAvailable is false off Darwin: the clone attributes are an APFS
// notion read through getattrlist (ADR-0074).
func CloneFactsAvailable() bool { return false }
