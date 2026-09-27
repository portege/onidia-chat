package main

// stt_selftest.go - a headless end-to-end check of the speech-input chain.
//
// Speech input fails in ways the UI cannot explain: the recorder links to the
// wrong node, the take is empty, the backend rejects the format, or the model
// simply hears nothing. This runs the whole chain once, prints every step, and
// exits - so the failure is visible without clicking the mic and reading a
// one-line message in the input bar.
//
//go run . -stt-test          # record ~4s, transcribe, print every step
//go run . -stt-test -stt-debug   # also print the backend's own diagnostics

import (
	"context"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/portege/chat-app/internal/mic"
)

const sttSelfTestSeconds = 4.0

// runSTTSelfTest records one take, transcribes it with the configured
// backend, and prints the whole chain. It returns a process exit code.
func runSTTSelfTest(backend STT, recorder, device string, debug bool) int {
	fmt.Println("== chat-app speech-input self test ==")
	if backend == nil {
		fmt.Println("backend  : speech input is OFF (stt = off)")
		return 2
	}
	fmt.Printf("backend  : %s\n", backend.Name())
	if recorder == "" {
		fmt.Println("recorder : NONE found (tried pw-record, parecord, arecord, ffmpeg)")
		return 2
	}
	fmt.Printf("recorder : %s\n", recorder)

	// What the app would record from, and what else exists.
	mics, listErr := mic.List(recorder)
	if listErr != nil {
		fmt.Printf("devices  : %v\n", listErr)
	} else {
		fmt.Printf("devices  :%s\n", mic.Describe(mics))
	}
	selected, found := mic.Pick(mics, device)
	if !found {
		fmt.Println("devices  : none available")
	} else {
		if device == "" {
			device = selected.Device
			fmt.Printf("device   : %s (default: %s)\n", device, selected.Desc)
		} else {
			fmt.Printf("device   : %s (stt-device; resolved: %s)\n", device, selected.Desc)
		}
	}

	fmt.Printf("\nrecording %.1fs - SPEAK NOW ...\n", sttSelfTestSeconds)
	rec, err := startSTTRecorder(recorder, device)
	if err != nil {
		fmt.Printf("record   : FAILED: %v\n", err)
		return 2
	}
	defer rec.cleanup()
	time.Sleep(sttSelfTestSeconds * time.Second)
	path, stopErr := rec.Stop()

	if info, err := os.Stat(path); err == nil {
		fmt.Printf("file     : %s (%d bytes)\n", path, info.Size())
	} else {
		fmt.Printf("file     : %s (stat error: %v)\n", path, err)
	}
	if stopErr != nil {
		fmt.Printf("recorder : exited with: %v\n", stopErr)
	}
	level, lvlErr := mic.Level(path)
	switch {
	case lvlErr != nil:
		fmt.Printf("level    : UNREADABLE: %v\n", lvlErr)
	case level.Silent:
		fmt.Printf("level    : %s\n", level)
		fmt.Println("            -> nothing reached the device: fix the mic before blaming the backend")
		fmt.Println("            -> re-run with -stt-debug, or check the chat-app log, for the full trace")
		return 1
	default:
		fmt.Printf("level    : %s\n", level)
	}

	fmt.Println("\ntranscribing (this can take a while on a Pi)...")
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()
	start := time.Now()
	text, err := backend.Transcribe(ctx, path)
	took := time.Since(start).Round(time.Millisecond)

	fmt.Println()
	if err != nil {
		fmt.Printf("RESULT   : ERROR after %s\n           %v\n", took, err)
		if debug {
			fmt.Println("\n(hint: the backend logged its own diagnostics above)")
		}
		return 2
	}
	if strings.TrimSpace(text) == "" {
		fmt.Printf("RESULT   : EMPTY transcript after %s\n", took)
		fmt.Println("           the backend accepted the audio but recognised no speech;")
		fmt.Println("           try a bigger model (-stt-whisper-model small) or a louder mic")
		return 1
	}
	fmt.Printf("RESULT   : %s\ntext     : %q\n", took, text)
	return 0
}
