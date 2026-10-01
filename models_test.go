package main

import "testing"

func TestPerModelThresholds(t *testing.T) {
	old := P.DB
	P.DB = t.TempDir() + "/t.db"
	defer func() { P.DB = old }()
	st, err := openStore()
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	if got := diarizeOptsFrom(st, 0); got.Model != "resnet34" || got.Threshold != 0.5 {
		t.Fatalf("defaults: %+v", got)
	}
	st.SetSetting("diarize_threshold", "0.45") // existing installs: resnet34 keeps its old setting
	st.SetSetting("diarize_model", "titanet-large")
	if got := diarizeOptsFrom(st, 0); got.Threshold != 0.96 {
		t.Fatalf("titanet-large default: %+v", got)
	}
	st.SetSetting(thresholdSetting("titanet-large"), "0.62")
	if got := diarizeOptsFrom(st, 0); got.Threshold != 0.62 {
		t.Fatalf("titanet-large stored: %+v", got)
	}
	if got := defaultThresholdFor("resnet34", st); got != 0.45 {
		t.Fatalf("resnet34 stored: %v", got)
	}
	for _, k := range diarizeModelKeys() {
		if diarizeModels[k] == "" || diarizeModelSizeMB[k] == 0 {
			t.Fatalf("model %s incomplete", k)
		}
	}
	if providerNow() != "cpu" && !gpuLibsInstalled() {
		t.Fatal("GPU provider without GPU libraries")
	}
}

func TestErrorLines(t *testing.T) {
	out := "\x1b[1;31m2026 [E:onnxruntime:, x.cc:1 Create] Failed to load library a.so with error: libcuda.so.1: cannot open\nterminate called\n  what():  boom\nSIGABRT: abort\nrip 0x1\n"
	if got := errorLines(out); got != "Failed to load library a.so with error: libcuda.so.1: cannot open | what():  boom" {
		t.Fatalf("got %q", got)
	}
}
