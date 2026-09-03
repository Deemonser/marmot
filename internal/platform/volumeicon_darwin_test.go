//go:build darwin

package platform

import (
	"bytes"
	"testing"
)

// The boot volume always has an icon, and the adapter must hand it back as a
// real PNG: the application layer wraps the bytes in a data URL unchecked.
func TestVolumeIconReturnsAPNGForTheBootVolume(t *testing.T) {
	png, err := Adapter{}.VolumeIcon("/", 64)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.HasPrefix(png, []byte("\x89PNG\r\n\x1a\n")) {
		t.Fatalf("not a PNG: % x", png[:min(8, len(png))])
	}
	if len(png) < 500 {
		t.Fatalf("suspiciously small icon: %d bytes", len(png))
	}
}

func TestVolumeIconRejectsAbsurdSizes(t *testing.T) {
	for _, pixels := range []int{0, -1, 4096} {
		if _, err := (Adapter{}).VolumeIcon("/", pixels); err == nil {
			t.Fatalf("size %d accepted", pixels)
		}
	}
}
