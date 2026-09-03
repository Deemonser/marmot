package platform

import (
	"encoding/json"
	"os"
	"path/filepath"

	"example.com/marmot/internal/ports"
)

// Scan totals live in a plain JSON file in the app's support directory: they
// are byte counts of the user's own disk, not secrets, so unlike the advisor
// credentials they are neither encrypted nor worth the machinery to be.
const scanTotalsFile = "scan-totals.json"

func scanTotalsPath() (string, error) {
	base, err := os.UserConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(base, "marmot", scanTotalsFile), nil
}

func readScanTotals(path string) map[string]ports.ScanTotal {
	raw, err := os.ReadFile(path)
	if err != nil {
		return map[string]ports.ScanTotal{}
	}
	totals := map[string]ports.ScanTotal{}
	if json.Unmarshal(raw, &totals) == nil {
		return totals
	}
	// The first format was a bare byte count per root. Read it as bytes with
	// no node history rather than as "no history": the byte denominator is
	// still good, only the node half is missing until the next completed walk.
	legacy := map[string]int64{}
	if json.Unmarshal(raw, &legacy) == nil {
		for root, bytes := range legacy {
			totals[root] = ports.ScanTotal{Bytes: bytes}
		}
		return totals
	}
	// A corrupt history file downgrades the next bar to the statfs
	// denominator; it must never fail a scan.
	return map[string]ports.ScanTotal{}
}

// LoadScanTotal returns the last completed walk's final counts for this root,
// or the zero value when there is no history.
func (a Adapter) LoadScanTotal(root string) ports.ScanTotal {
	path, err := scanTotalsPath()
	if err != nil {
		return ports.ScanTotal{}
	}
	return readScanTotals(path)[root]
}

// StoreScanTotal records a completed walk's final counts for its root.
func (a Adapter) StoreScanTotal(root string, total ports.ScanTotal) error {
	if root == "" || total.Bytes <= 0 {
		return nil
	}
	path, err := scanTotalsPath()
	if err != nil {
		return err
	}
	totals := readScanTotals(path)
	totals[root] = total
	encoded, err := json.Marshal(totals)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	temp, err := os.CreateTemp(filepath.Dir(path), ".scan-totals-*")
	if err != nil {
		return err
	}
	defer os.Remove(temp.Name())
	if err := temp.Chmod(0o600); err != nil {
		temp.Close()
		return err
	}
	if _, err := temp.Write(encoded); err != nil {
		temp.Close()
		return err
	}
	if err := temp.Close(); err != nil {
		return err
	}
	return os.Rename(temp.Name(), path)
}
