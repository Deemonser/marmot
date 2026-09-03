//go:build !darwin

package platform

import "errors"

func (Adapter) VolumeIcon(string, int) ([]byte, error) {
	return nil, errors.New("volume icons are only available on macOS")
}
