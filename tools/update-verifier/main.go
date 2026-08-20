package main

import (
	"context"
	"encoding/json"
	"flag"
	"io"
	"os"
	"time"

	"github.com/jinpeng2700-tech/cpa-plugin-token-saver/tools/update-verifier/verifier"
)

func main() {
	os.Exit(run(os.Args[1:], os.Stdin, os.Stdout))
}

func run(args []string, input io.Reader, output io.Writer) int {
	flags := flag.NewFlagSet("update-verifier", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	baseURL := flags.String("base-url", "http://127.0.0.1:8317", "literal-loopback CLIProxyAPI base URL")
	approvalPath := flags.String("approval", "", "root-owned approved-artifacts.json")
	cliPath := flags.String("cli", "", "installed CLIProxyAPI binary")
	pluginPath := flags.String("plugin", "", "installed Token Saver shared library")
	phaseValue := flags.String("phase", string(verifier.PhasePreflight), "preflight or postinstall")
	credentialStdin := flags.Bool("credential-stdin", false, "read management credential from stdin")
	if errParse := flags.Parse(args); errParse != nil || flags.NArg() != 0 {
		return writeResult(output, blockedResult(verifier.CodeApprovalInvalid))
	}
	phase := verifier.Phase(*phaseValue)
	if phase != verifier.PhasePreflight && phase != verifier.PhasePostInstall {
		return writeResult(output, blockedResult(verifier.CodeApprovalInvalid))
	}
	approval, approvalCode := verifier.LoadApprovalFile(*approvalPath)
	if approvalCode != verifier.CodeOK {
		return writeResult(output, blockedResult(approvalCode))
	}
	var credential string
	var credentialCode string
	if *credentialStdin {
		credential, credentialCode = verifier.ReadCredentialFrom(input)
	} else {
		credential, credentialCode = verifier.LoadCredential()
	}
	if credentialCode != verifier.CodeOK {
		return writeResult(output, blockedResult(credentialCode))
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	result := verifier.Verify(ctx, verifier.Options{
		BaseURL: *baseURL, Credential: credential, Approval: approval, Phase: phase,
		CLIPath: *cliPath, PluginPath: *pluginPath,
	})
	return writeResult(output, result)
}

func blockedResult(code string) verifier.Result {
	return verifier.Result{
		SchemaVersion: verifier.VerifierSchemaVersion, Classification: verifier.ClassificationBlocked, Code: code,
	}
}

func writeResult(output io.Writer, result verifier.Result) int {
	_ = json.NewEncoder(output).Encode(result)
	if result.Compatible {
		return 0
	}
	if result.Classification == verifier.ClassificationCandidateFailure {
		return 3
	}
	return 2
}
