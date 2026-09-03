package application

import (
	"errors"
	"strings"
	"testing"

	"example.com/marmot/internal/ports"
)

type staticVolumeIcons struct {
	png   []byte
	err   error
	calls int
}

func (icons *staticVolumeIcons) VolumeIcon(string, int) ([]byte, error) {
	icons.calls++
	return icons.png, icons.err
}

func TestGetStorageSourcesCarriesTheSystemIconAsADataURL(t *testing.T) {
	icons := &staticVolumeIcons{png: []byte("PNG")}
	service := NewService(Dependencies{
		Volumes: staticVolumeCatalog{items: []ports.Volume{{ID: "root", Name: "Macintosh HD", Path: "/", Kind: "internal", Role: "system", Scannable: true, TotalBytes: 10, UsedBytes: 5, FreeBytes: 5}}},
		Icons:   icons,
	})
	sources, err := service.GetStorageSources()
	if err != nil {
		t.Fatal(err)
	}
	if len(sources) != 1 || sources[0].Icon != "data:image/png;base64,UE5H" {
		t.Fatalf("icon not carried as a data URL: %+v", sources)
	}
	if _, err := service.GetStorageSources(); err != nil {
		t.Fatal(err)
	}
	if icons.calls != 1 {
		t.Fatalf("icon fetched %d times; the source list is re-read after every scan and the icon does not change", icons.calls)
	}
}

func TestGetStorageSourcesWithoutIconsOrOnFailureLeavesTheIconEmpty(t *testing.T) {
	items := []ports.Volume{{ID: "root", Name: "Macintosh HD", Path: "/", Kind: "internal", Role: "system", Scannable: true, TotalBytes: 10, UsedBytes: 5, FreeBytes: 5}}
	for name, deps := range map[string]Dependencies{
		"no port": {Volumes: staticVolumeCatalog{items: items}},
		"failure": {Volumes: staticVolumeCatalog{items: items}, Icons: &staticVolumeIcons{err: errors.New("no icon")}},
	} {
		sources, err := NewService(deps).GetStorageSources()
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if len(sources) != 1 || strings.TrimSpace(sources[0].Icon) != "" {
			t.Fatalf("%s: expected an empty icon, got %+v", name, sources)
		}
	}
}
