//go:build !darwin

package platform

import "errors"

func Trash(string) (string, error) { return "", errors.New("Trash is only available on macOS") }
