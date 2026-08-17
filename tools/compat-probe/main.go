package main

import (
	"context"
	"encoding/json"
	"flag"
	"io"
	"os"
	"time"

	"github.com/router-for-me/cpa-plugin-token-saver/tools/compat-probe/probe"
)

func main() {
	os.Exit(run(os.Args[1:], os.Stdout))
}

func run(args []string, output io.Writer) int {
	flags := flag.NewFlagSet("compat-probe", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	mode := flags.String("mode", string(probe.ModePlugin), "plugin or core-only compatibility mode")
	candidate := flags.String("candidate", "", "CLIProxyAPI candidate binary")
	plugin := flags.String("plugin", "", "versioned Token Saver Linux shared library")
	timeout := flags.Duration("timeout", 45*time.Second, "bounded probe timeout (maximum 60s)")
	if errParse := flags.Parse(args); errParse != nil || flags.NArg() != 0 {
		report := probe.Report{SchemaVersion: probe.SchemaVersion, Code: probe.CodeCandidateInvalid}
		_ = json.NewEncoder(output).Encode(report)
		return 2
	}
	report := probe.Run(context.Background(), probe.Options{Mode: probe.Mode(*mode), CandidatePath: *candidate, PluginPath: *plugin, Timeout: *timeout})
	_ = json.NewEncoder(output).Encode(report)
	if report.Compatible {
		return 0
	}
	return 2
}
