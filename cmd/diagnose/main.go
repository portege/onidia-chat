// diagnose - onidia-chat troubleshooting tool.
//
// Runs the same diagnosis the in-app "Diagnose" dialog runs: system, hardware,
// installation, settings (credentials masked), network, proxy, all model
// providers, audio/soundcard, TTS, and dependencies. Nothing is transmitted;
// the report is printed locally for the user to copy, save, or send.
//
// \tdiagnose                 # human-readable report to stdout
// \tdiagnose -json           # machine-readable JSON report
// \tdiagnose -output report.txt
// \tdiagnose -quick          # skip live network probes
//
// Exit codes: 0 = report emitted, 2 = error while building/writing the report.
//
// A failed check in the report is not an error: it is a finding with a hint,
// exactly as onidia-chat's preflight surfaces issues without blocking.
package main

import (
	"bytes"
	"context"
	"flag"
	"fmt"
	"io"
	"os"
	"time"

	"github.com/portege/onidia-chat/internal/diagnose"
)

func main() { os.Exit(run(os.Args[1:], os.Stdout, os.Stderr)) }

func run(args []string, out, errOut io.Writer) int {
	fs := flag.NewFlagSet("diagnose", flag.ContinueOnError)
	fs.SetOutput(errOut)
	var (
		jsonFlag   = fs.Bool("json", false, "emit machine-readable JSON report")
		outputFlag = fs.String("output", "", "write report to this file instead of stdout")
		quickFlag  = fs.Bool("quick", false, "skip live network probes")
	)
	if err := fs.Parse(args); err != nil {
		return 2
	}

	ctx := context.Background()
	if *quickFlag {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, 2*time.Second)
		defer cancel()
	}

	rep := diagnose.Collect(ctx)

	var b bytes.Buffer
	if *jsonFlag {
		if err := rep.JSON(&b); err != nil {
			fmt.Fprintf(errOut, "diagnose: json: %v\n", err)
			return 2
		}
	} else {
		if err := rep.Text(&b); err != nil {
			fmt.Fprintf(errOut, "diagnose: text: %v\n", err)
			return 2
		}
	}

	if *outputFlag != "" {
		if err := os.WriteFile(*outputFlag, b.Bytes(), 0o644); err != nil {
			fmt.Fprintf(errOut, "diagnose: write %s: %v\n", *outputFlag, err)
			return 2
		}
		return 0
	}
	_, _ = out.Write(b.Bytes())
	return 0
}
