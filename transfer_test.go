package main

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
)

func TestExportImportRoundTrip(t *testing.T) {
	oldP := P
	defer func() { P = oldP }()

	// computer A with one podcast, one audio file and its own gpu settings
	os.Setenv("QSPODSCRIPT_HOME", t.TempDir())
	defer os.Unsetenv("QSPODSCRIPT_HOME")
	if err := initPaths(); err != nil {
		t.Fatal(err)
	}
	a, err := openStore()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := a.AddFeed(Feed{URL: "https://x/feed.xml", Title: "Show A", Language: "en"}); err != nil {
		t.Fatal(err)
	}
	a.SetSetting("whisper_backend", "vulkan")
	a.SetSetting("diarize_threshold", "0.45")
	os.WriteFile(filepath.Join(P.Audio, "ep1_v1.ogg"), []byte("OggS fake"), 0o644)
	var buf bytes.Buffer
	if err := exportTo(&buf, a); err != nil {
		t.Fatal(err)
	}
	a.Close()

	// computer B: own data + cuda
	os.Setenv("QSPODSCRIPT_HOME", t.TempDir())
	if err := initPaths(); err != nil {
		t.Fatal(err)
	}
	b, _ := openStore()
	b.AddFeed(Feed{URL: "https://y/other.xml", Title: "Show B"})
	b.SetSetting("whisper_backend", "cuda")
	b.Close()

	zipPath := filepath.Join(P.Work, "in.zip")
	os.WriteFile(zipPath, buf.Bytes(), 0o644)
	info, err := stageImport(zipPath)
	if err != nil || info.Podcasts != 1 {
		t.Fatalf("stage: %+v %v", info, err)
	}
	if err := applyStagedImport(); err != nil {
		t.Fatal(err)
	}
	st, err := openStore()
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	feeds, _ := st.Feeds()
	if len(feeds) != 1 || feeds[0].Title != "Show A" {
		t.Fatalf("feeds after import: %+v", feeds)
	}
	if got := st.Setting("whisper_backend", ""); got != "cuda" {
		t.Fatalf("machine setting not kept: %q", got)
	}
	if got := st.Setting("diarize_threshold", ""); got != "0.45" {
		t.Fatalf("data setting not imported: %q", got)
	}
	if !fileExists(filepath.Join(P.Audio, "ep1_v1.ogg")) {
		t.Fatal("audio missing")
	}
	backups, _ := filepath.Glob(filepath.Join(P.Data, "before-import-*", "qs-podscript.db"))
	if len(backups) != 1 {
		t.Fatalf("backup of B's data missing: %v", backups)
	}
	if _, ok := pendingImport(); ok {
		t.Fatal("staging not cleaned up")
	}
}

func TestImportRejectsOtherZips(t *testing.T) {
	oldP := P
	defer func() { P = oldP }()
	os.Setenv("QSPODSCRIPT_HOME", t.TempDir())
	defer os.Unsetenv("QSPODSCRIPT_HOME")
	initPaths()
	p := filepath.Join(P.Work, "x.zip")
	os.WriteFile(p, []byte("not a zip"), 0o644)
	if _, err := stageImport(p); err == nil {
		t.Fatal("accepted garbage")
	}
}
