// Bento4 mp4dash / mp4hls wrapper.
package packager

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"log"
	"os/exec"
	"strconv"
	"strings"

	"github.com/muntader/zaynin-engine/pkg/encoder/media/packager/drm"
)

type HLSSettings struct {
	Container string
	Version   int
}
type Options struct {
	VideoFiles             []string
	AudioTracks            []AudioTrackInfo
	SubtitleTracks         []SubtitleTrackInfo
	OutputDir              string
	Format                 string
	DRM                    *drm.DRMKeys
	SegmentDurationSeconds int
	HLSSettings            HLSSettings
}
type AudioTrackInfo struct {
	Language   string `json:"language"`
	Label      string `json:"label"`
	OutputFile string `json:"output_file"`
	Default    bool   `json:"default,omitempty"`
	AutoSelect bool   `json:"auto_select,omitempty"`
}
type SubtitleTrackInfo struct {
	Language   string `json:"language"`
	Label      string `json:"label"`
	OutputFile string `json:"output_file"`
	Default    bool   `json:"default,omitempty"`
	AutoSelect bool   `json:"auto_select,omitempty"`
}

type Bento4Packager struct {
	mp4dashPath string
	mp4hlsPath  string
}

func NewBento4Packager(mp4dashPath, mp4hlsPath string) *Bento4Packager {
	return &Bento4Packager{
		mp4dashPath: mp4dashPath,
		mp4hlsPath:  mp4hlsPath,
	}
}

// Package picks mp4hls for AES-128 HLS, mp4dash for everything else.
func (p *Bento4Packager) Package(ctx context.Context, opts Options) (*exec.Cmd, error) {
	var toolPath string
	var args []string

	isSimpleAES := opts.DRM != nil && opts.DRM.IsSimpleAES

	// aes-128 hls → mp4hls; dash/cmaf/fairplay/cenc → mp4dash
	if isSimpleAES && opts.Format == "hls" {
		log.Println("INFO: AES-128 encryption detected. Routing to mp4hls for traditional HLS (.ts) packaging.")
		if p.mp4hlsPath == "" {
			return nil, fmt.Errorf("mp4hls path is not configured, but is required for AES-128 encryption")
		}
		toolPath = p.mp4hlsPath
		args = p.buildMp4hlsArgs(opts)
	} else {
		log.Println("INFO: Using mp4dash for DASH, CMAF, or CENC/FairPlay-encrypted HLS packaging.")
		if p.mp4dashPath == "" {
			return nil, fmt.Errorf("mp4dash path is not configured")
		}
		toolPath = p.mp4dashPath
		args = p.buildMp4dashArgs(opts)
	}

	cmd := exec.CommandContext(ctx, toolPath, args...)
	return cmd, nil
}

type argBuilder struct{ args []string }

func newArgBuilder() *argBuilder         { return &argBuilder{args: []string{}} }
func (b *argBuilder) add(args ...string) { b.args = append(b.args, args...) }

func (p *Bento4Packager) buildMp4hlsArgs(opts Options) []string {
	builder := newArgBuilder()

	// Add output directory
	builder.add("--output-dir", opts.OutputDir)

	// Add segment and HLS settings
	if opts.SegmentDurationSeconds > 0 {
		builder.add("--segment-duration", strconv.Itoa(opts.SegmentDurationSeconds))
	}
	if opts.HLSSettings.Version > 0 {
		builder.add("--hls-version", strconv.Itoa(opts.HLSSettings.Version))
	}

	// Add encryption settings for AES-128
	if keys := opts.DRM; keys != nil && keys.IsSimpleAES {
		log.Println("INFO: Applying AES-128 encryption settings for mp4hls.")
		if keys.AESKeyURI == "" {
			log.Println("ERROR: AESKeyURI is required for HLS AES-128 encryption. The playlist will be invalid.")
		}
		builder.add("--encryption-key", keys.ContentKey)
		builder.add("--encryption-key-uri", keys.AESKeyURI)
		log.Printf(" -> Encryption Key: [REDACTED], Key URI: %s", keys.AESKeyURI)
	}

	// Add all media files as positional arguments
	// mp4hls muxes audio and video together into the TS segments.
	log.Println("INFO: Adding media inputs for mp4hls.")
	for _, videoFile := range opts.VideoFiles {
		builder.add(videoFile)
		log.Printf(" -> Video: %s", videoFile)
	}
	for _, track := range opts.AudioTracks {
		builder.add(track.OutputFile)
		log.Printf(" -> Audio: %s", track.OutputFile)
	}
	// mp4hls muxes a/v into ts   no sidecar webvtt
	if len(opts.SubtitleTracks) > 0 {
		log.Println("WARN: Subtitle tracks are ignored when using mp4hls. They should be added to the master playlist manually as sidecar files.")
	}

	return builder.args
}

func (p *Bento4Packager) buildMp4dashArgs(opts Options) []string {
	builder := newArgBuilder()

	builder.add("--output-dir", opts.OutputDir)

	if opts.Format == "hls" || opts.Format == "cmaf" {
		builder.add("--hls")
		log.Println("INFO: HLS manifest generation is enabled via mp4dash --hls flag.")
		log.Println(" -> Using fMP4 format for HLS segments (mp4dash default).")
	}

	// Add packaging settings (HLS version, etc.)
	b := builder
	isHlsOutput := opts.Format == "hls" || opts.Format == "cmaf"
	if isHlsOutput {
		log.Println(" -> Applying HLS-specific settings for mp4dash.")
		if opts.HLSSettings.Version > 0 {
			versionStr := strconv.Itoa(opts.HLSSettings.Version)
			b.add("--hls-version", versionStr)
			log.Printf("    -> Setting HLS version to %s.", versionStr)
		}
	}

	// CENC / FairPlay only here   AES-128 goes through mp4hls
	builder.addDRMForMp4dash(opts.DRM, opts.Format)

	// Add media inputs with rich selectors
	builder.addMediaInputsForMp4dash(opts)

	return builder.args
}

func (b *argBuilder) addMediaInputsForMp4dash(opts Options) {
	boolToYesNo := func(v bool) string {
		if v {
			return "YES"
		}
		return "NO"
	}
	isHlsOutput := opts.Format == "hls" || opts.Format == "cmaf"
	for _, videoFile := range opts.VideoFiles {
		b.add(videoFile)
	}
	for _, track := range opts.AudioTracks {
		var arg string
		if isHlsOutput {
			arg = fmt.Sprintf(`[type=audio,+hls_default=%s,+hls_autoselect=%s,+language_name=%s,+language=%s]%s`,
				boolToYesNo(track.Default), boolToYesNo(track.AutoSelect), track.Label, track.Language, track.OutputFile)
		} else {
			arg = fmt.Sprintf(`[type=audio,+language=%s,+language_name=%s]%s`, track.Language, track.Label, track.OutputFile)
		}
		b.add(arg)
	}
	for _, track := range opts.SubtitleTracks {
		var arg string
		if isHlsOutput {
			arg = fmt.Sprintf(`[+hls_group=subtitles,+hls_default=%s,+hls_autoselect=%s,+format=webvtt,+language_name=%s,+language=%s]%s`,
				boolToYesNo(track.Default), boolToYesNo(track.AutoSelect), track.Label, track.Language, track.OutputFile)
		} else {
			arg = fmt.Sprintf(`[+format=webvtt,+language=%s,+language_name=%s]%s`, track.Language, track.Label, track.OutputFile)
		}
		b.add(arg)
	}
}

func (b *argBuilder) addDRMForMp4dash(keys *drm.DRMKeys, format string) {
	if keys == nil {
		return
	}

	log.Println("INFO: Adding multi-DRM encryption arguments for mp4dash.")

	hasWidevine := keys.PSSH["WIDEVINE"] != ""
	hasPlayReady := keys.PSSH["PLAYREADY"] != ""
	hasFairPlay := keys.IsFairPlay
	applyFairPlaySettings := hasFairPlay && (format == "hls" || format == "cmaf")

	if applyFairPlaySettings {
		b.add("--encryption-cenc-scheme=cbcs")
		log.Println(" -> Using 'cbcs' encryption scheme for FairPlay compatibility.")
	} else {
		b.add("--encryption-cenc-scheme=cenc")
		log.Println(" -> Using 'cenc' encryption scheme for Widevine/PlayReady.")
	}

	kidWithoutHyphens := strings.ReplaceAll(keys.KID, "-", "")
	var ivHex string
	if keys.IV != "" {
		ivHex = keys.IV
	} else if applyFairPlaySettings {
		ivBytes := make([]byte, 16)
		rand.Read(ivBytes)
		ivHex = hex.EncodeToString(ivBytes)
		log.Println(" -> Generating new random IV for FairPlay.")
	}

	keySpec := fmt.Sprintf("%s:%s", kidWithoutHyphens, keys.ContentKey)
	if ivHex != "" {
		keySpec = fmt.Sprintf("%s:%s:%s", kidWithoutHyphens, keys.ContentKey, ivHex)
		log.Println(" -> Using key spec with explicit IV.")
	}
	b.add("--encryption-key", keySpec)
	log.Printf(" -> Using KID:%s for encryption key.", kidWithoutHyphens)

	if hasWidevine {
		b.add("--widevine-header", "#"+keys.PSSH["WIDEVINE"])
		log.Println(" -> Added Widevine PSSH.")
	}
	if hasPlayReady {
		b.add("--playready-header", "#"+keys.PSSH["PLAYREADY"])
		log.Println(" -> Added PlayReady PSSH.")
	}

	if applyFairPlaySettings {
		b.add("--fairplay-key-uri", keys.SkdURI)
		log.Println(" -> Added FairPlay KEY-URI.")
	}
}
