//go:build !linux

package verifier

import "os"

func trustedApprovalFile(os.FileInfo) bool { return false }
