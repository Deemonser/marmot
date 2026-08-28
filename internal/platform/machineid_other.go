//go:build !darwin

package platform

import "os"

// Non-darwin is a placeholder platform port; the hostname is a stand-in so the
// credential store compiles and behaves, not a claim that it is equivalent.
func machineIdentifier() (string, error) {
	return os.Hostname()
}
