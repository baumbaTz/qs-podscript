package main

import (
	"archive/zip"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"strings"
	"time"
)

// Moving to another computer: export = database + audio folder in one zip;
// import = replace everything on this computer with such a zip. Tools and
// models are not included - setup downloads them on the new computer.
//
// Import is two steps so the running app never swaps its open database:
// the zip is checked and unpacked into data/import-staging, and the next
// start moves it in place (the previous data goes to data/before-import-<time>).
// Settings that belong to the computer (graphics card, speech engine build,
// downloaded model) are kept from the computer, not taken from the zip.

const (
	exportManifest = "qs-podscript-export.json"
	exportDBName   = "qs-podscript.db"
)

var machineSettings = []string{"whisper_release", "whisper_backend", "gpu_name", "gpu_mode",
	"setup_check", "whisper_flash_attn", "whisper_model"}

type exportInfo struct {
	App      string `json:"app"`
	Version  string `json:"version"`
	Schema   int    `json:"schema"`
	Created  string `json:"created"`
	Podcasts int    `json:"podcasts"`
	Episodes int    `json:"episodes_transcribed"`
}

func stagingDir() string { return filepath.Join(P.Data, "import-staging") }

// exportTo writes the export zip. The database is copied with VACUUM INTO, a
// consistent snapshot even while QS-PodScript keeps working.
func exportTo(w io.Writer, st *Store) error {
	snap := filepath.Join(P.Work, fmt.Sprintf("export-%d.db", time.Now().UnixNano()))
	defer os.Remove(snap)
	if _, err := st.db.Exec(`VACUUM INTO ?`, snap); err != nil {
		return fmt.Errorf("copy database: %w", err)
	}
	info := exportInfo{App: "QS-PodScript", Version: version, Schema: schemaVersion, Created: time.Now().Format(time.RFC3339)}
	st.db.QueryRow(`SELECT COUNT(*) FROM feeds`).Scan(&info.Podcasts)
	st.db.QueryRow(`SELECT COUNT(*) FROM episodes WHERE active_version_id IS NOT NULL`).Scan(&info.Episodes)

	zw := zip.NewWriter(w)
	mw, err := zw.Create(exportManifest)
	if err != nil {
		return err
	}
	enc := json.NewEncoder(mw)
	enc.SetIndent("", "  ")
	if err := enc.Encode(info); err != nil {
		return err
	}
	if err := addFileToZip(zw, snap, exportDBName, zip.Deflate); err != nil {
		return err
	}
	entries, err := os.ReadDir(P.Audio)
	if err != nil {
		return err
	}
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".ogg") {
			continue
		}
		// audio is already compressed: store it as is
		if err := addFileToZip(zw, filepath.Join(P.Audio, e.Name()), "audio/"+e.Name(), zip.Store); err != nil {
			return err
		}
	}
	return zw.Close()
}

func addFileToZip(zw *zip.Writer, src, name string, method uint16) error {
	f, err := os.Open(src)
	if err != nil {
		return err
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil {
		return err
	}
	h, err := zip.FileInfoHeader(fi)
	if err != nil {
		return err
	}
	h.Name, h.Method = name, method
	w, err := zw.CreateHeader(h)
	if err != nil {
		return err
	}
	_, err = io.Copy(w, f)
	return err
}

// stageImport checks an export zip and unpacks it into the staging folder.
func stageImport(zipPath string) (exportInfo, error) {
	var info exportInfo
	zr, err := zip.OpenReader(zipPath)
	if err != nil {
		return info, fmt.Errorf("not a QS-PodScript export (%v)", err)
	}
	defer zr.Close()
	var hasDB bool
	for _, f := range zr.File {
		switch {
		case f.Name == exportManifest:
			rc, err := f.Open()
			if err != nil {
				return info, err
			}
			err = json.NewDecoder(rc).Decode(&info)
			rc.Close()
			if err != nil {
				return info, fmt.Errorf("export description unreadable: %v", err)
			}
		case f.Name == exportDBName:
			hasDB = true
		}
	}
	if info.App == "" || !hasDB {
		return info, fmt.Errorf("this file is not a QS-PodScript export")
	}
	if info.Schema > schemaVersion {
		return info, fmt.Errorf("the export comes from a newer QS-PodScript (%s) - update this computer first", info.Version)
	}

	os.RemoveAll(stagingDir())
	if err := os.MkdirAll(filepath.Join(stagingDir(), "audio"), 0o755); err != nil {
		return info, err
	}
	for _, f := range zr.File {
		var dst string
		switch {
		case f.Name == exportDBName:
			dst = filepath.Join(stagingDir(), exportDBName)
		case strings.HasPrefix(f.Name, "audio/") && !f.FileInfo().IsDir():
			base := path.Base(f.Name)
			// only plain file names: no way to write outside the folder
			if base != f.Name[len("audio/"):] || base == "." || base == ".." || strings.ContainsAny(base, `/\:`) {
				continue
			}
			dst = filepath.Join(stagingDir(), "audio", base)
		default:
			continue
		}
		if err := extractZipFile(f, dst); err != nil {
			os.RemoveAll(stagingDir())
			return info, fmt.Errorf("unpacking %s: %v", f.Name, err)
		}
	}
	// the database must open
	db, err := sql.Open("sqlite3", filepath.Join(stagingDir(), exportDBName))
	if err == nil {
		var n int
		err = db.QueryRow(`SELECT COUNT(*) FROM feeds`).Scan(&n)
		db.Close()
	}
	if err != nil {
		os.RemoveAll(stagingDir())
		return info, fmt.Errorf("the database in the export is damaged: %v", err)
	}
	b, _ := json.Marshal(info)
	if err := os.WriteFile(filepath.Join(stagingDir(), "READY"), b, 0o644); err != nil {
		return info, err
	}
	return info, nil
}

func extractZipFile(f *zip.File, dst string) error {
	rc, err := f.Open()
	if err != nil {
		return err
	}
	defer rc.Close()
	out, err := os.Create(dst)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, rc); err != nil {
		out.Close()
		return err
	}
	return out.Close()
}

// pendingImport returns the staged import waiting for a restart, if any.
func pendingImport() (exportInfo, bool) {
	var info exportInfo
	b, err := os.ReadFile(filepath.Join(stagingDir(), "READY"))
	if err != nil {
		return info, false
	}
	json.Unmarshal(b, &info)
	return info, true
}

func cancelImport() error { return os.RemoveAll(stagingDir()) }

// applyStagedImport runs at start, before the database is opened.
func applyStagedImport() error {
	info, ok := pendingImport()
	if !ok {
		return nil
	}
	backup := filepath.Join(P.Data, "before-import-"+time.Now().Format("2006-01-02-150405"))
	if err := os.MkdirAll(backup, 0o755); err != nil {
		return err
	}
	// current data -> backup
	for _, suffix := range []string{"", "-wal", "-shm"} {
		if fileExists(P.DB + suffix) {
			if err := os.Rename(P.DB+suffix, filepath.Join(backup, filepath.Base(P.DB)+suffix)); err != nil {
				return fmt.Errorf("import: moving the current database aside: %w", err)
			}
		}
	}
	if err := os.Rename(P.Audio, filepath.Join(backup, "audio")); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("import: moving the current audio aside: %w", err)
	}
	// imported data -> place
	if err := os.Rename(filepath.Join(stagingDir(), exportDBName), P.DB); err != nil {
		return fmt.Errorf("import: %w", err)
	}
	if err := os.Rename(filepath.Join(stagingDir(), "audio"), P.Audio); err != nil {
		return fmt.Errorf("import: %w", err)
	}
	os.RemoveAll(stagingDir())
	keepMachineSettings(filepath.Join(backup, filepath.Base(P.DB)), P.DB)
	logf("Imported data from another computer (QS-PodScript %s, %s, %d podcasts, %d transcribed episodes). The previous data is in %s",
		info.Version, info.Created, info.Podcasts, info.Episodes, backup)
	return nil
}

// keepMachineSettings copies this computer's graphics card / engine settings
// from the previous database into the imported one.
func keepMachineSettings(oldDB, newDB string) {
	if !fileExists(oldDB) {
		return
	}
	src, err := sql.Open("sqlite3", oldDB)
	if err != nil {
		return
	}
	defer src.Close()
	dst, err := sql.Open("sqlite3", newDB)
	if err != nil {
		return
	}
	defer dst.Close()
	for _, k := range machineSettings {
		var v string
		if src.QueryRow(`SELECT value FROM settings WHERE key=?`, k).Scan(&v) != nil {
			dst.Exec(`DELETE FROM settings WHERE key=?`, k) // unknown here: let setup decide
			continue
		}
		dst.Exec(`INSERT INTO settings(key,value) VALUES(?,?) ON CONFLICT(key) DO UPDATE SET value=excluded.value`, k, v)
	}
}
