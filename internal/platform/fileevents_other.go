//go:build !darwin

package platform

import (
	"errors"
	"time"

	"example.com/marmot/internal/ports"
)

func (Adapter) WatchFileEvents(string, time.Duration, func([]ports.FileEvent)) (func(), error) {
	return nil, errors.New("file event watching is only available on macOS")
}
