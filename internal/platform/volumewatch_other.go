//go:build !darwin

package platform

import "errors"

func (Adapter) WatchVolumes(func()) (func(), error) {
	return nil, errors.New("volume watching is only available on macOS")
}
