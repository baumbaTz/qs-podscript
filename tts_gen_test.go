package main

import (
	"encoding/binary"
	"fmt"
	"math/rand"
	"os"
	"os/exec"
	"testing"

	sherpa "github.com/k2-fsa/sherpa-onnx-go/sherpa_onnx"
)

// Generates an English 4-host test "podcast" with known speakers (env TTSGEN=1).
func TestGenPodcast(t *testing.T) {
	if os.Getenv("TTSGEN") == "" {
		t.Skip()
	}
	d := "/tmp/vits-piper-en_US-libritts_r-medium/"
	c := &sherpa.OfflineTtsConfig{}
	c.Model.Vits.Model = d + "en_US-libritts_r-medium.onnx"
	c.Model.Vits.Tokens = d + "tokens.txt"
	c.Model.Vits.DataDir = d + "espeak-ng-data"
	c.Model.Vits.NoiseScale, c.Model.Vits.NoiseScaleW, c.Model.Vits.LengthScale = 0.667, 0.8, 1.0
	c.Model.NumThreads = 1
	tts := sherpa.NewOfflineTts(c)
	if tts == nil {
		t.Fatal("tts")
	}
	hosts := []int{3, 120, 409, 777} // four distinct voices
	long := []string{
		"So this week we watched a movie that I have been putting off for years, and honestly I was surprised.",
		"I think the second half completely falls apart, the pacing is all over the place.",
		"The practical effects hold up really well though, you can tell they built most of those sets.",
		"Did anyone else notice that the villain disappears for almost forty minutes?",
		"I actually went back and checked the box office numbers and it did much better than I expected.",
		"My favourite scene is the one in the diner, the dialogue there is just fantastic.",
		"Okay but can we talk about that ending, because I have questions.",
		"I remember seeing this in the theatre when I was a kid and it scared me to death.",
	}
	short := []string{"Yeah.", "Right, right.", "Totally.", "No way!", "Exactly."}
	seed := int64(7)
	if v := os.Getenv("SEED"); v != "" {
		fmt.Sscan(v, &seed)
	}
	rng := rand.New(rand.NewSource(seed))
	var out []float32
	var truth []string
	sr := 0
	add := func(spk int, text string) {
		a := tts.Generate(text, hosts[spk], 1.0)
		sr = a.SampleRate
		start := float64(len(out)) / float64(sr)
		out = append(out, a.Samples...)
		truth = append(truth, fmt.Sprintf("%.2f %.2f %d", start, float64(len(out))/float64(sr), spk))
		out = append(out, make([]float32, sr*3/10)...) // 0.3 s pause
	}
	prev := -1
	for i := 0; i < 70; i++ {
		spk := rng.Intn(4)
		if spk == prev {
			spk = (spk + 1) % 4
		}
		add(spk, long[rng.Intn(len(long))])
		if rng.Intn(3) == 0 { // backchannel from someone else
			add((spk+1+rng.Intn(3))%4, short[rng.Intn(len(short))])
		}
		prev = spk
	}
	// write 16-bit wav at tts rate, convert to 16 kHz with ffmpeg
	f, _ := os.Create("/tmp/pod_raw.wav")
	hdr := make([]byte, 44)
	copy(hdr[0:], "RIFF")
	binary.LittleEndian.PutUint32(hdr[4:], uint32(36+len(out)*2))
	copy(hdr[8:], "WAVEfmt ")
	binary.LittleEndian.PutUint32(hdr[16:], 16)
	binary.LittleEndian.PutUint16(hdr[20:], 1)
	binary.LittleEndian.PutUint16(hdr[22:], 1)
	binary.LittleEndian.PutUint32(hdr[24:], uint32(sr))
	binary.LittleEndian.PutUint32(hdr[28:], uint32(sr*2))
	binary.LittleEndian.PutUint16(hdr[32:], 2)
	binary.LittleEndian.PutUint16(hdr[34:], 16)
	copy(hdr[36:], "data")
	binary.LittleEndian.PutUint32(hdr[40:], uint32(len(out)*2))
	f.Write(hdr)
	b := make([]byte, 2)
	for _, s := range out {
		v := int16(max(-1, min(1, s)) * 32767)
		binary.LittleEndian.PutUint16(b, uint16(v))
		f.Write(b)
	}
	f.Close()
	exec.Command("ffmpeg", "-y", "-loglevel", "error", "-i", "/tmp/pod_raw.wav", "-ar", "16000", "-ac", "1", os.Getenv("OUT")+".wav").Run()
	tf, _ := os.Create(os.Getenv("OUT") + "_truth.txt")
	for _, l := range truth {
		fmt.Fprintln(tf, l)
	}
	tf.Close()
	fmt.Printf("generated %.0f s, %d turns\n", float64(len(out))/float64(sr), len(truth))
}
