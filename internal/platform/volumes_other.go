//go:build !darwin

package platform

import "example.com/marmot/internal/ports"

func (Adapter) ListVolumes() ([]ports.Volume, error) {
	return []ports.Volume{{ID: "/", Name: "当前文件系统", Path: "/", Kind: "unsupported", Permission: "unsupported", Message: "第一阶段只支持 macOS", Scannable: false}}, nil
}
