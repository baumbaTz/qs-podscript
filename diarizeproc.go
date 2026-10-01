package main

// Speaker detection runs in a child process ("qs-podscript diarize-file"):
// the detection itself is C code that can't be interrupted, so "Stop now"
// had to wait until it finished. A child process is simply ended. As a bonus
// a crash inside it (graphics card) doesn't end QS-PodScript - the episode
// is then done on the CPU.

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
)

type diarizeRequest struct {
	Opts     DiarizeOpts `json:"opts"`
	Provider string      `json:"provider"`
}

type diarizeResponse struct {
	Turns []Turn             `json:"turns"`
	Embs  []ClusterEmbedding `json:"embs"`
	Error string             `json:"error,omitempty"`
}

// forcedProvider: set in the child - the parent already decided CPU or GPU.
var forcedProvider string

// diarizeWav: speaker detection of a 16 kHz WAV file, stoppable via ctx.
func diarizeWav(ctx context.Context, wav string, o DiarizeOpts) ([]Turn, []ClusterEmbedding, error) {
	prov := providerNow()
	turns, embs, err := runDiarizeChild(ctx, wav, o, prov)
	if err != nil && ctx.Err() == nil && prov != "cpu" {
		logf("   speaker detection failed on the graphics card (%v) - doing it on the CPU", err)
		markGPUBroken(err.Error())
		return runDiarizeChild(ctx, wav, o, "cpu")
	}
	return turns, embs, err
}

func runDiarizeChild(ctx context.Context, wav string, o DiarizeOpts, provider string) ([]Turn, []ClusterEmbedding, error) {
	exe, err := os.Executable()
	if err != nil {
		return nil, nil, err
	}
	req, _ := json.Marshal(diarizeRequest{Opts: o, Provider: provider})
	out := wav + ".speakers.json"
	defer os.Remove(out)
	cmd := exec.CommandContext(ctx, exe, "diarize-file", wav, out, string(req))
	var stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = io.Discard, &stderr
	err = cmd.Run()
	if ctx.Err() != nil {
		return nil, nil, ctx.Err()
	}
	if err != nil {
		return nil, nil, fmt.Errorf("speaker detection stopped unexpectedly (%v): %s", err, errorLines(stderr.String()))
	}
	b, err := os.ReadFile(out)
	if err != nil {
		return nil, nil, fmt.Errorf("speaker detection gave no result: %v", err)
	}
	var res diarizeResponse
	if err := json.Unmarshal(b, &res); err != nil {
		return nil, nil, fmt.Errorf("speaker detection result unreadable: %v", err)
	}
	if res.Error != "" {
		return nil, nil, fmt.Errorf("%s", res.Error)
	}
	return res.Turns, res.Embs, nil
}

// cmdDiarizeFile (child): qs-podscript diarize-file <wav> <out.json> <request-json>
func cmdDiarizeFile(args []string) error {
	if len(args) != 3 {
		return fmt.Errorf("internal command")
	}
	var req diarizeRequest
	if err := json.Unmarshal([]byte(args[2]), &req); err != nil {
		return err
	}
	forcedProvider = req.Provider
	if forcedProvider == "" {
		forcedProvider = "cpu"
	}
	var res diarizeResponse
	samples, err := readWav16k(args[0])
	if err == nil {
		res.Turns, res.Embs, err = diarize(samples, req.Opts)
	}
	if err != nil {
		res.Error = err.Error()
	}
	b, _ := json.Marshal(res)
	return os.WriteFile(args[1], b, 0o644)
}
