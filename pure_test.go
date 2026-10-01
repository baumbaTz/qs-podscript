package main

import (
	"bufio"
	"fmt"
	"os"
	"sort"
	"testing"

	sherpa "github.com/k2-fsa/sherpa-onnx-go/sherpa_onnx"
)

func TestPureEmbeddings(t *testing.T) {
	if os.Getenv("MODELS") == "" {
		t.Skip()
	}
	P.Models = os.Getenv("MODELS")
	s, _ := readWav16k("/tmp/pod.wav")
	type seg struct {
		a, b float64
		spk  int
	}
	var truth []seg
	f, _ := os.Open("/tmp/pod_truth.txt")
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		var g seg
		fmt.Sscanf(sc.Text(), "%f %f %d", &g.a, &g.b, &g.spk)
		if g.b-g.a > 3 {
			truth = append(truth, g)
		}
	}
	f.Close()
	for _, model := range []string{"wespeaker_en_voxceleb_resnet34_LM.onnx", "wespeaker_en_voxceleb_CAM++_LM.onnx"} {
		ex := sherpa.NewSpeakerEmbeddingExtractor(&sherpa.SpeakerEmbeddingExtractorConfig{Model: P.Models + "/" + model, NumThreads: 1, Provider: "cpu"})
		emb := func(segs []seg) []float64 {
			st := ex.CreateStream()
			for _, g := range segs {
				st.AcceptWaveform(16000, s[int(g.a*16000):int(g.b*16000)])
			}
			st.InputFinished()
			v := ex.Compute(st)
			sherpa.DeleteOnlineStream(st)
			return normalize(v)
		}
		// single utterances
		var vs [][]float64
		var who []int
		for i, g := range truth {
			if i >= 24 {
				break
			}
			vs = append(vs, emb([]seg{g}))
			who = append(who, g.spk)
		}
		var same, diff []float64
		for i := range vs {
			for j := i + 1; j < len(vs); j++ {
				if who[i] == who[j] {
					same = append(same, cosine(vs[i], vs[j]))
				} else {
					diff = append(diff, cosine(vs[i], vs[j]))
				}
			}
		}
		sort.Float64s(same)
		sort.Float64s(diff)
		fmt.Printf("%s single ~5s utterances: same %.2f..%.2f (median %.2f) | different %.2f..%.2f (median %.2f)\n", model,
			same[0], same[len(same)-1], same[len(same)/2], diff[0], diff[len(diff)-1], diff[len(diff)/2])
		// 30 s aggregates per speaker, two disjoint halves
		agg := map[int][][]seg{}
		for _, g := range truth {
			k := len(agg[g.spk])
			_ = k
		}
		halves := map[int][2][]seg{}
		for _, g := range truth {
			h := halves[g.spk]
			if len(h[0]) <= len(h[1]) {
				h[0] = append(h[0], g)
			} else {
				h[1] = append(h[1], g)
			}
			halves[g.spk] = h
		}
		var hv [][]float64
		var hw []int
		for spk := 0; spk < 4; spk++ {
			for k := 0; k < 2; k++ {
				segs := halves[spk][k]
				if len(segs) > 6 {
					segs = segs[:6]
				}
				hv = append(hv, emb(segs))
				hw = append(hw, spk)
			}
		}
		same, diff = nil, nil
		for i := range hv {
			for j := i + 1; j < len(hv); j++ {
				if hw[i] == hw[j] {
					same = append(same, cosine(hv[i], hv[j]))
				} else {
					diff = append(diff, cosine(hv[i], hv[j]))
				}
			}
		}
		sort.Float64s(same)
		sort.Float64s(diff)
		fmt.Printf("%s ~30s aggregates: same %.2f..%.2f | different %.2f..%.2f\n", model, same[0], same[len(same)-1], diff[0], diff[len(diff)-1])
		sherpa.DeleteSpeakerEmbeddingExtractor(ex)
	}
}
