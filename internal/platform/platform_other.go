//go:build !linux && !darwin && !windows

package platform

import (
	"fmt"
	"syscall"

	"pyexec/pkg/types"
)

// TraceDeps is not implemented for this platform.
func TraceDeps(binaryPath string) ([]types.SystemDep, error) {
	return nil, fmt.Errorf("TraceDeps not supported on this platform")
}

// NamespaceSysProcAttr returns nil on unsupported platforms.
func NamespaceSysProcAttr() *syscall.SysProcAttr {
	return nil
}
