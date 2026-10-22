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
	"strings"

	"github.com/portege/onidia-chat/internal/confpath"
	"github.com/portege/onidia-chat/internal/diagnose"
	"github.com/portege/onidia-chat/internal/preflight"
)

func main() { os.Exit(run(os.Args[1:], os.Stdout, os.Stderr)) }

func run(args []string, out, errOut io.Writer) int {
	fs := flag.NewFlagSet("diagnose", flag.ContinueOnError)
	fs.SetOutput(errOut)
	var (
		jsonFlag       = fs.Bool("json", false, "emit machine-readable JSON report")
		outputFlag     = fs.String("output", "", "write report to this file instead of stdout")
		quickFlag      = fs.Bool("quick", false, "skip live network probes")
		configFlag     = fs.String("config", "", "INI config file (default: same search order as onidia-chat)")
		providerFlag   = fs.String("provider", "", "LLM provider override")
		apiKeyFlag     = fs.String("api-key", "", "provider API key override")
		apiURLFlag     = fs.String("api-url", "", "provider API endpoint override")
		awsProfileFlag = fs.String("aws-profile", "", "AWS profile override")
		awsRegionFlag  = fs.String("aws-region", "", "AWS region override")
		ttsFlag        = fs.String("tts", "", "TTS on/off override")
		ttsKeyFlag     = fs.String("tts-key", "", "TTS API key override")
	)
	if err := fs.Parse(args); err != nil {
		return 2
	}

	configPath := strings.TrimSpace(*configFlag)
	if configPath == "" {
		configPath = confpath.Default()
	}
	settings := resolveDiagnosticSettings(configPath, *providerFlag, *apiKeyFlag, *apiURLFlag,
		*awsProfileFlag, *awsRegionFlag, *ttsFlag, *ttsKeyFlag)

	rep := diagnose.CollectWithOptions(context.Background(), diagnose.Options{
		ConfigPath: configPath, Settings: settings, SkipNetwork: *quickFlag,
	})

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

	if _, err := out.Write(b.Bytes()); err != nil {
		fmt.Fprintf(errOut, "diagnose: write report: %v\n", err)
		return 2
	}
	return 0
}

func resolveDiagnosticSettings(path, providerFlag, apiKeyFlag, apiURLFlag,
	awsProfileFlag, awsRegionFlag, ttsFlag, ttsKeyFlag string) map[string]string {
	kv := map[string]string{}
	if path != "" {
		kv, _ = preflight.ReadINI(path)
	}
	provider := strings.ToLower(strings.TrimSpace(providerFlag))
	if provider == "" {
		provider = strings.ToLower(strings.TrimSpace(preflight.Get(kv, "provider")))
	}
	if provider == "" {
		provider = "gemini"
	}
	switch provider {
	case "gemini", "bedrock", "ollama", "openrouter":
	default:
		provider = "gemini"
	}
	settings := map[string]string{"provider": provider}
	apiKey := strings.TrimSpace(apiKeyFlag)
	if apiKey == "" {
		switch provider {
		case "gemini":
			apiKey = firstDiagnosticValue(os.Getenv("GEMINI_API_KEY"), os.Getenv("GOOGLE_API_KEY"))
			if apiKey == "" {
				apiKey = preflight.Get(kv, "api-key")
			}
			if apiKey == "" {
				apiKey = "built-in default"
			}
			settings["gemini-api-key"] = apiKey
		case "openrouter":
			apiKey = strings.TrimSpace(os.Getenv("OPENROUTER_API_KEY"))
			if apiKey == "" {
				apiKey = preflight.Get(kv, "api-key")
			}
			settings["openrouter-api-key"] = apiKey
		}
	} else {
		switch provider {
		case "gemini":
			settings["gemini-api-key"] = apiKey
		case "openrouter":
			settings["openrouter-api-key"] = apiKey
		}
	}
	if apiURLFlag != "" {
		switch provider {
		case "gemini":
			settings["gemini-api-url"] = apiURLFlag
		case "openrouter":
			settings["openrouter-api-url"] = apiURLFlag
		case "ollama":
			settings["ollama-api-url"] = apiURLFlag
		}
	}
	if awsProfileFlag != "" {
		settings["aws-profile"] = awsProfileFlag
	}
	if awsRegionFlag != "" {
		settings["aws-region"] = awsRegionFlag
	}
	tts := strings.TrimSpace(ttsFlag)
	if tts == "" {
		tts = preflight.Get(kv, "tts")
	}
	settings["tts"] = tts
	ttsKey := strings.TrimSpace(ttsKeyFlag)
	if ttsKey == "" {
		ttsKey = firstDiagnosticValue(os.Getenv("TYPECAST_API_KEY"), preflight.Get(kv, "tts-key"))
	}
	if ttsKey == "" {
		ttsKey = "built-in default"
	}
	if ttsKey != "" {
		settings["tts-key"] = ttsKey
	}
	return settings
}

func firstDiagnosticValue(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}
