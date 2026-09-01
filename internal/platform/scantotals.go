package platform

import (
	"encoding/json"
	"os"
	"path/filepath"
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

func readScanTotals(path string) map[string]int64 {
	raw, err := os.ReadFile(path)
	if err != nil {
		return map[string]int64{}
	}
	totals := map[string]int64{}
	if json.Unmarshal(raw, &totals) != nil {
		// A corrupt history file downgrades the next bar to the statfs
		// denominator; it must never fail a scan.
		return map[string]int64{}
	}
	return totals
}

// LoadScanTotal returns the last completed walk's final counted bytes for this
// root, or 0 when there is no history.
func (a Adapter) LoadScanTotal(root string) int64 {
	path, err := scanTotalsPath()
	if err != nil {
		return 0
	}
	return readScanTotals(path)[root]
}

// StoreScanTotal records a completed walk's final counted bytes for its root.
func (a Adapter) StoreScanTotal(root string, bytes int64) error {
	if root == "" || bytes <= 0 {
		return nil
	}
	path, err := scanTotalsPath()
	if err != nil {
		return err
	}
	totals := readScanTotals(path)
	totals[root] = bytes
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
