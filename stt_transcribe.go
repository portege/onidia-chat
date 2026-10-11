// stt_transcribe.go - the Amazon Transcribe streaming backend (the default).
//
// Uses the same AWS credential chain and profile/region as the Bedrock
// provider, so there is no second set of credentials to configure. Audio is
// sent as headerless 16 kHz mono PCM, which is what the service requires, in
// the ~1 second chunks its docs ask for.
//
// The call is a bidirectional event stream: results arrive while audio is
// still being sent, so partial transcripts are kept separately and only the
// final (isPartial = false) results are joined - returning both would repeat
// every phrase twice.

package main

import (
	"context"
	"errors"
	"log"
	"path/filepath"
	"strings"

	"github.com/aws/aws-sdk-go-v2/aws"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/transcribestreaming"
	ts "github.com/aws/aws-sdk-go-v2/service/transcribestreaming/types"
)

const (
	// sttDefaultLanguage is what Transcribe is asked for unless the config
	// says otherwise. AWS uses codes like en-US, not "eng" as TTS does.
	sttDefaultLanguage = "en-US"
	// sttDefaultRegion matches the app's Bedrock default, so a user who never
	// set a region still gets working credentials.
	sttDefaultRegion = "us-east-1"
	// sttMinAudio rejects taps and key clicks: a fraction of a second has
	// nothing recognisable in it.
	sttMinAudio = 0.4
)

type transcribeSTT struct {
	language string
	profile  string
	region   string
	debug    bool // log the full request/response trace
}

// newTranscribeSTT validates the settings. AWS credentials are resolved lazily
// on the first take, so a machine that never records neither pays for the
// lookup nor logs AWS noise at startup.
func newTranscribeSTT(language, profile, region string, debug bool) (STT, error) {
	language = strings.TrimSpace(language)
	if language == "" {
		language = sttDefaultLanguage
	}
	if region = strings.TrimSpace(region); region == "" {
		region = sttDefaultRegion
	}
	return &transcribeSTT{language: language, profile: strings.TrimSpace(profile), region: region, debug: debug}, nil
}

func (t *transcribeSTT) Name() string { return "transcribe" }

func (t *transcribeSTT) Transcribe(ctx context.Context, wavPath string) (string, error) {
	pcm, rate, _, err := wavPCM(wavPath)
	if err != nil {
		return "", err
	}
	if rate != sttSampleRate {
		return "", sttErrf("recorded audio is %d Hz but Transcribe needs %d Hz", rate, sttSampleRate)
	}
	if secs := float64(len(pcm)) / float64(sttSampleRate*2); secs < sttMinAudio {
		return "", sttErrf("that was too short to transcribe (%.1fs)", secs)
	}

	cfg, err := t.load(ctx)
	if err != nil {
		return "", err
	}
	client := transcribestreaming.NewFromConfig(cfg)

	out, err := client.StartStreamTranscription(ctx, &transcribestreaming.StartStreamTranscriptionInput{
		LanguageCode:                      ts.LanguageCode(t.language),
		MediaEncoding:                     ts.MediaEncodingPcm,
		MediaSampleRateHertz:              aws.Int32(int32(sttSampleRate)),
		EnablePartialResultsStabilization: true,
	})
	if err != nil {
		return "", transcribeError(err)
	}
	log.Printf("stt: transcribe %s %s -> %d bytes (%.2fs, profile=%q region=%q debug=%v)",
		t.language, filepath.Base(wavPath), len(pcm),
		float64(len(pcm))/float64(sttSampleRate*2), t.profile, t.region, t.debug)
	stream := out.GetStream()

	// Feed audio from its own goroutine so the receive loop below can run
	// concurrently - the service errors out on an unread event stream.
	sendErr := make(chan error, 1)
	go func() {
		chunk := sttChunkSeconds * sttSampleRate * 2
		for off := 0; off < len(pcm); off += chunk {
			end := off + chunk
			if end > len(pcm) {
				end = len(pcm)
			}
			err := stream.Send(ctx, &ts.AudioStreamMemberAudioEvent{
				Value: ts.AudioEvent{AudioChunk: pcm[off:end]},
			})
			if err != nil {
				sendErr <- err
				return
			}
		}
		sendErr <- stream.Close()
	}()

	var final []string
	var partial strings.Builder
	for event := range stream.Events() {
		ev, ok := event.(*ts.TranscriptResultStreamMemberTranscriptEvent)
		if !ok || ev.Value.Transcript == nil {
			continue
		}
		for _, res := range ev.Value.Transcript.Results {
			if len(res.Alternatives) == 0 || res.Alternatives[0].Transcript == nil {
				continue
			}
			text := strings.TrimSpace(*res.Alternatives[0].Transcript)
			if text == "" {
				continue
			}
			if res.IsPartial {
				// Each partial replaces the one before it, so only the last
				// is worth keeping as a fallback.
				partial.Reset()
				partial.WriteString(text)
				continue
			}
			final = append(final, text)
		}
	}
	if err := stream.Err(); err != nil {
		return "", transcribeError(err)
	}
	select {
	case err := <-sendErr:
		if err != nil {
			return "", transcribeError(err)
		}
	default:
	}

	if text := strings.TrimSpace(strings.Join(final, " ")); text != "" {
		return text, nil
	}
	// A very short take can be flushed as a partial only, never finalised.
	if text := strings.TrimSpace(partial.String()); text != "" {
		return text, nil
	}
	log.Printf("stt: transcribe returned NO TEXT for %.2fs of audio", float64(len(pcm))/float64(sttSampleRate*2))
	return "", sttErrf("nothing recognised in that recording - try again a little closer to the mic")
}

// load resolves the AWS config on first use, honouring the shared profile.
func (t *transcribeSTT) load(ctx context.Context) (aws.Config, error) {
	opts := []func(*awsconfig.LoadOptions) error{awsconfig.WithRegion(t.region)}
	if t.profile != "" {
		opts = append(opts, awsconfig.WithSharedConfigProfile(t.profile))
	}
	cfg, err := awsconfig.LoadDefaultConfig(ctx, opts...)
	if err != nil {
		return cfg, sttErrf("cannot load AWS credentials (profile %q): %v", t.profile, err)
	}
	return cfg, nil
}

// transcribeError turns the AWS failures a user can actually fix into an
// STTError carrying a useful hint; anything else is logged and passed through.
func transcribeError(err error) error {
	if err == nil {
		return nil
	}
	var bad *ts.BadRequestException
	if errors.As(err, &bad) {
		return sttErrf("Transcribe rejected the audio: %s", aws.ToString(bad.Message))
	}
	// The service reports a missing permission as a plain AccessDenied, not a
	// modelled exception, so match on the message. This is the single most
	// common setup failure, and the fix (an IAM action) is worth naming.
	msg := err.Error()
	if strings.Contains(msg, "AccessDeniedException") {
		return sttErrf("AWS denied Transcribe - add transcribe:StartStreamTranscription to this user's " +
			"IAM policy, or set stt = whisper to transcribe locally")
	}
	if strings.Contains(msg, "ExpiredToken") || strings.Contains(msg, "InvalidClientTokenId") {
		return sttErrf("AWS credentials are not usable: %s - refresh them, or set stt = whisper", firstLine(msg))
	}
	log.Printf("stt: transcribe failed: %v", err)
	return err
}
