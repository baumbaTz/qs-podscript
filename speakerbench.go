package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	sherpa "github.com/k2-fsa/sherpa-onnx-go/sherpa_onnx"
)

// cmdSpeakerBench: run speaker detection with several models on the same
// audio and print time + number of voices, to compare models (and CPU vs
// graphics card) without transcribing anything.
//
//	qs-podscript speaker-bench <audio file> [model,model,...] [--cpu]
func cmdSpeakerBench(args []string) error {
	var files, models []string
	forceCPU := false
	var thresholds []float32
	for _, a := range args {
		switch {
		case strings.HasPrefix(a, "--threshold="):
			for _, v := range strings.Split(strings.TrimPrefix(a, "--threshold="), ",") {
				var f float32
				if _, err := fmt.Sscanf(v, "%g", &f); err == nil && f > 0 {
					thresholds = append(thresholds, f)
				}
			}
		case a == "--cpu":
			forceCPU = true
		case strings.Contains(a, ",") || diarizeModels[a] != "":
			models = append(models, strings.Split(a, ",")...)
		default:
			files = append(files, a)
		}
	}
	if len(files) == 0 {
		return fmt.Errorf("usage: qs-podscript speaker-bench <audio file> [model,model,...] [--cpu]\nmodels: %s", strings.Join(diarizeModelKeys(), ", "))
	}
	if len(models) == 0 {
		for _, k := range diarizeModelKeys() {
			if fileExists(diarizeModelPath(k)) {
				models = append(models, k)
			}
		}
	}
	st, err := openStore()
	if err != nil {
		return err
	}
	defer st.Close()
	setSpeakerDevice(st.Setting("speaker_device", deviceAuto))
	provider := "cpu"
	if !forceCPU {
		provider = speakerProvider(st)
	}
	fmt.Printf("Speaker detection runs on: %s\n", providerText(provider))
	for _, f := range files {
		samples, cleanup, err := benchAudio(f)
		if err != nil {
			return err
		}
		defer cleanup()
		secs := float64(len(samples)) / sampleRate
		fmt.Printf("\n%s (%s of audio)\n", filepath.Base(f), fmtDur(secs))
		for _, m := range models {
			if diarizeModels[m] == "" {
				fmt.Printf("  %-10s unknown model\n", m)
				continue
			}
			if !fileExists(diarizeModelPath(m)) {
				fmt.Printf("  %-10s downloading…\n", m)
				if err := downloadFile(context.Background(), speakerModelsURL+diarizeModels[m], diarizeModelPath(m), "speaker model "+m); err != nil {
					return err
				}
			}
			ths := thresholds
			if len(ths) == 0 {
				ths = []float32{defaultThresholdFor(m, st)}
			}
			for _, th := range ths {
				o := diarizeOptsFrom(st, 0)
				o.Model = m
				o.Threshold = th
				c := diarizationConfig(o)
				c.Segmentation.Provider, c.Embedding.Provider = provider, provider
				t0 := time.Now()
				sd := sherpa.NewOfflineSpeakerDiarization(c)
				if sd == nil {
					fmt.Printf("  %-10s could not load the model\n", m)
					continue
				}
				segs := sd.Process(samples)
				sherpa.DeleteOfflineSpeakerDiarization(sd)
				el := time.Since(t0).Seconds()
				voices := map[int]float64{}
				for _, s := range segs {
					voices[s.Speaker] += float64(s.End - s.Start)
				}
				var share []float64
				for _, v := range voices {
					share = append(share, v)
				}
				sort.Sort(sort.Reverse(sort.Float64Slice(share)))
				var top []string
				for i, v := range share {
					if i == 5 {
						break
					}
					top = append(top, fmtDur(v))
				}
				fmt.Printf("  %-10s %7.1fs  (%.1fx real time)  %2d voices, threshold %.2f  largest: %s\n",
					m, el, secs/el, len(voices), o.Threshold, strings.Join(top, ", "))
			}
		}
	}
	return nil
}

func fmtDur(s float64) string {
	if s >= 3600 {
		return fmt.Sprintf("%dh%02dm", int(s)/3600, int(s)%3600/60)
	}
	if s >= 60 {
		return fmt.Sprintf("%dm%02ds", int(s)/60, int(s)%60)
	}
	return fmt.Sprintf("%.1fs", s)
}

// benchAudio: 16 kHz mono samples of any audio file (converted with ffmpeg
// unless it already is a 16 kHz mono WAV).
func benchAudio(path string) ([]float32, func(), error) {
	if s, err := readWav16k(path); err == nil {
		return s, func() {}, nil
	}
	tmp, err := os.MkdirTemp("", "qsps-bench")
	if err != nil {
		return nil, nil, err
	}
	cleanup := func() { os.RemoveAll(tmp) }
	wav := filepath.Join(tmp, "a.wav")
	if err := runFFmpeg(context.Background(), "-i", path, "-vn", "-ar", "16000", "-ac", "1", "-c:a", "pcm_s16le", wav); err != nil {
		cleanup()
		return nil, nil, err
	}
	s, err := readWav16k(wav)
	if err != nil {
		cleanup()
		return nil, nil, err
	}
	return s, cleanup, nil
}
