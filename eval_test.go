package main

import (
	"bufio"
	"fmt"
	"os"
	"sort"
	"testing"
)

func TestEvalMerge(t *testing.T) {
	if os.Getenv("MODELS") == "" {
		t.Skip()
	}
	P.Models = os.Getenv("MODELS")
	s, _ := readWav16k("/tmp/pod.wav")
	type seg struct {
		a, b int64
		spk  int
	}
	var truth []seg
	f, _ := os.Open("/tmp/pod_truth.txt")
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		var a, b float64
		var k int
		fmt.Sscanf(sc.Text(), "%f %f %d", &a, &b, &k)
		truth = append(truth, seg{int64(a * 1000), int64(b * 1000), k})
	}
	f.Close()
	for _, cfg := range []DiarizeOpts{
		{Model: "resnet34", Step: 0.1, Threshold: 0.3},
		{Model: "resnet34", Step: 0.25, Threshold: 0.3},
		{Model: "resnet34", Step: 0.1, Threshold: 0.5},
	} {
		turns, embs, _ := diarize(s, cfg)
		// true speaker per cluster = majority overlap
		ov := map[int]map[int]int64{}
		talk := map[int]int64{}
		for _, tu := range turns {
			talk[tu.Cluster] += tu.EndMs - tu.StartMs
			for _, g := range truth {
				lo, hi := max64(tu.StartMs, g.a), min64(tu.EndMs, g.b)
				if hi > lo {
					if ov[tu.Cluster] == nil {
						ov[tu.Cluster] = map[int]int64{}
					}
					ov[tu.Cluster][g.spk] += hi - lo
				}
			}
		}
		who := map[int]int{}
		var impure int64
		for c, m := range ov {
			best, bv, tot := -1, int64(0), int64(0)
			for k, v := range m {
				tot += v
				if v > bv {
					best, bv = k, v
				}
			}
			who[c] = best
			impure += tot - bv
		}
		fmt.Printf("\n%s: %d clusters (truth 4); time in wrong cluster %.1fs\n", cfg, len(embs), float64(impure)/1000)
		// similarity stats: same vs different true speaker, raw and centered
		dim := len(embs[0].Vec)
		mean := make([]float64, dim)
		var ns [][]float64
		for _, e := range embs {
			n := normalize(e.Vec)
			ns = append(ns, n)
			for i := range n {
				mean[i] += n[i] / float64(len(embs))
			}
		}
		var sameR, diffR, sameC, diffC []float64
		for i := range embs {
			for j := i + 1; j < len(embs); j++ {
				a, b := make([]float64, dim), make([]float64, dim)
				for k := range a {
					a[k], b[k] = ns[i][k]-mean[k], ns[j][k]-mean[k]
				}
				r, cc := cosine(ns[i], ns[j]), cosine(a, b)
				if who[embs[i].Cluster] == who[embs[j].Cluster] {
					sameR, sameC = append(sameR, r), append(sameC, cc)
				} else {
					diffR, diffC = append(diffR, r), append(diffC, cc)
				}
			}
		}
		mm := func(x []float64) string {
			if len(x) == 0 {
				return "-"
			}
			sort.Float64s(x)
			return fmt.Sprintf("min %.2f max %.2f (n=%d)", x[0], x[len(x)-1], len(x))
		}
		fmt.Printf("  raw      same speaker: %s | different: %s\n", mm(sameR), mm(diffR))
		fmt.Printf("  centered same speaker: %s | different: %s\n", mm(sameC), mm(diffC))
		for _, th := range []float64{0.88, 0.9, 0.92, 0.94} {
			ms := planVoiceMerges(embs, talk, th)
			wrong := 0
			for _, m := range ms {
				if who[m.From] != who[m.Into] {
					wrong++
				}
			}
			fmt.Printf("  merge raw>=%.2f: %d -> %d voices, wrong merges %d\n", th, len(embs), len(embs)-len(ms), wrong)
		}
	}
}
