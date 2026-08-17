package verifier

import (
	"io"
	"os"
)

const maxApprovalBytes = 64 << 10

func LoadApprovalFile(path string) (Approval, string) {
	info, errLstat := os.Lstat(path)
	if errLstat != nil {
		return Approval{}, CodeApprovalRead
	}
	if !trustedApprovalFile(info) {
		return Approval{}, CodeApprovalUntrusted
	}
	file, errOpen := os.Open(path)
	if errOpen != nil {
		return Approval{}, CodeApprovalRead
	}
	defer file.Close()
	raw, errRead := io.ReadAll(io.LimitReader(file, maxApprovalBytes+1))
	if errRead != nil || len(raw) > maxApprovalBytes {
		return Approval{}, CodeApprovalRead
	}
	return ParseApproval(raw)
}
