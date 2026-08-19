package verifier

import (
	"io"
	"os"
	"path/filepath"
	"strings"
)

const ManagementCredentialName = "cliproxyapi-management-key"
const maxCredentialBytes = 4096

func LoadCredential() (string, string) {
	return readCredential(os.Getenv, os.ReadFile)
}

func readCredential(getenv func(string) string, readFile func(string) ([]byte, error)) (string, string) {
	directory := strings.TrimSpace(getenv("CREDENTIALS_DIRECTORY"))
	if directory == "" {
		return "", CodeCredentialDirectory
	}
	raw, errRead := readFile(filepath.Join(directory, ManagementCredentialName))
	if errRead != nil {
		return "", CodeCredentialRead
	}
	return validateCredential(raw)
}

func ReadCredentialFrom(reader io.Reader) (string, string) {
	raw, errRead := io.ReadAll(io.LimitReader(reader, maxCredentialBytes+1))
	if errRead != nil {
		return "", CodeCredentialRead
	}
	if len(raw) > maxCredentialBytes {
		return "", CodeCredentialInvalid
	}
	return validateCredential(raw)
}

func validateCredential(raw []byte) (string, string) {
	credential := strings.TrimSpace(string(raw))
	if credential == "" || strings.ContainsAny(credential, "\r\n\x00") {
		return "", CodeCredentialInvalid
	}
	return credential, CodeOK
}
