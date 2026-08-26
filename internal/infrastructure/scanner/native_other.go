//go:build !darwin

package scanner

import (
	"context"

	"example.com/marmot/internal/domain/scan"
)

func scanConfiguredTree(ctx context.Context, root string, emit scan.BatchEmitter, phase scan.PhaseEmitter, resolveMounts MountResolver) (scan.Result, error) {
	return scanTree(ctx, root, func(node scan.Node) error {
		return emit([]scan.Node{node})
	}, phase, resolveMounts)
}
