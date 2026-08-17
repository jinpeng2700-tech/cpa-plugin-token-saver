package verifier

import (
	"os"
	"path/filepath"
	"strings"
)

const ManagementCredentialName = "cliproxyapi-management-key"

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
	credential := strings.TrimSpace(string(raw))
	if credential == "" || strings.ContainsAny(credential, "\r\n\x00") {
		return "", CodeCredentialInvalid
	}
	return credential, CodeOK
}
