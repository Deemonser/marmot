//go:build darwin

package platform

import "golang.org/x/sys/unix"

// machineIdentifier is the hardware UUID, read through sysctl rather than by
// shelling out to ioreg: the app does not hand strings to a shell (SDD §4), and
// this needs no cgo either.
func machineIdentifier() (string, error) {
	return unix.Sysctl("kern.uuid")
}
