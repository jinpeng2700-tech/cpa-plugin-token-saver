package main

import (
	"bytes"
	"strings"
	"testing"
)

func TestRunRejectsCredentialFlagWithoutEchoingValue(t *testing.T) {
	sentinel := "MANAGEMENT_SENTINEL_DO_NOT_LEAK"
	var output bytes.Buffer
	exitCode := run([]string{"-credential", sentinel}, strings.NewReader(""), &output)
	if exitCode == 0 {
		t.Fatal("run accepted a management credential flag")
	}
	if strings.Contains(output.String(), sentinel) {
		t.Fatalf("output leaked rejected credential: %s", output.String())
	}
	if !strings.Contains(output.String(), `"classification":"blocked"`) {
		t.Fatalf("output = %s, want stable blocked result", output.String())
	}
}
