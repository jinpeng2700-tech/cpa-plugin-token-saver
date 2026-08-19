package verifier

import (
	"bytes"
	"errors"
	"path/filepath"
	"strings"
	"testing"
)

func TestReadCredentialUsesOnlyFixedCredentialFile(t *testing.T) {
	sentinel := "MANAGEMENT_SENTINEL_DO_NOT_LEAK"
	var readPath string
	credential, code := readCredential(
		func(name string) string {
			if name != "CREDENTIALS_DIRECTORY" {
				t.Fatalf("unexpected environment lookup %q", name)
			}
			return filepath.Join("root", "credentials")
		},
		func(path string) ([]byte, error) {
			readPath = path
			return []byte(sentinel + "\n"), nil
		},
	)
	if code != CodeOK || credential != sentinel {
		t.Fatalf("readCredential() = %q, %q", credential, code)
	}
	if filepath.Base(readPath) != ManagementCredentialName {
		t.Fatalf("credential path = %q, want fixed name %q", readPath, ManagementCredentialName)
	}
}

func TestReadCredentialReturnsStableCodesWithoutSecret(t *testing.T) {
	for _, tt := range []struct {
		name    string
		dir     string
		payload string
		err     error
		code    string
	}{
		{name: "directory missing", code: CodeCredentialDirectory},
		{name: "read failure", dir: "credentials", err: errors.New("TOP_SECRET_READ_ERROR"), code: CodeCredentialRead},
		{name: "empty", dir: "credentials", payload: " \n", code: CodeCredentialInvalid},
		{name: "multiline", dir: "credentials", payload: "TOP_SECRET\nSECOND", code: CodeCredentialInvalid},
	} {
		t.Run(tt.name, func(t *testing.T) {
			credential, code := readCredential(func(string) string { return tt.dir }, func(string) ([]byte, error) {
				return []byte(tt.payload), tt.err
			})
			if credential != "" || code != tt.code {
				t.Fatalf("readCredential() = %q, %q; want empty, %q", credential, code, tt.code)
			}
			if strings.Contains(code, "TOP_SECRET") {
				t.Fatalf("stable code leaked secret: %q", code)
			}
		})
	}
}

func TestReadCredentialFromStdinAcceptsOneSecretLine(t *testing.T) {
	const sentinel = "MANAGEMENT_SENTINEL_DO_NOT_LEAK"
	credential, code := ReadCredentialFrom(bytes.NewBufferString(sentinel + "\n"))
	if credential != sentinel || code != CodeOK {
		t.Fatalf("ReadCredentialFrom() = %q, %q", credential, code)
	}
}

func TestReadCredentialFromStdinRejectsMultilineAndOversizedInput(t *testing.T) {
	for _, payload := range []string{
		"TOP_SECRET\nSECOND\n",
		strings.Repeat("x", maxCredentialBytes+1),
	} {
		credential, code := ReadCredentialFrom(strings.NewReader(payload))
		if credential != "" || code != CodeCredentialInvalid {
			t.Fatalf("ReadCredentialFrom() = %q, %q; want empty, %q", credential, code, CodeCredentialInvalid)
		}
	}
}
