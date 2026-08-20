//go:build !darwin

package platform

import "errors"

func (Adapter) Preview(string) (string, error) {
	return "", errors.New("Quick Look is only available on macOS")
}

func (Adapter) Reveal(string) (string, error) {
	return "", errors.New("Finder is only available on macOS")
}
