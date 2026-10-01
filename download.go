package main

import (
	"archive/tar"
	"archive/zip"
	"compress/bzip2"
	"compress/gzip"
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// downloadFile downloads url to dest via dest+".part", showing progress on the
// console. The final file only appears once the download completed.
func downloadFile(ctx context.Context, url, dest, label string) error {
	if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
	if err != nil {
		return err
	}
	req.Header.Set("User-Agent", userAgent)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return fmt.Errorf("download %s: %w", label, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return fmt.Errorf("download %s: HTTP %d from %s", label, resp.StatusCode, url)
	}
	part := dest + ".part"
	f, err := os.Create(part)
	if err != nil {
		return err
	}
	total := resp.ContentLength
	var done int64
	buf := make([]byte, 256<<10)
	lastShow := time.Time{}
	start := time.Now()
	for {
		n, rerr := resp.Body.Read(buf)
		if n > 0 {
			if _, werr := f.Write(buf[:n]); werr != nil {
				f.Close()
				os.Remove(part)
				return werr
			}
			done += int64(n)
			if time.Since(lastShow) > 300*time.Millisecond {
				lastShow = time.Now()
				if total > 0 {
					progressf("  %s: %d%% (%s / %s)", label, done*100/total, humanBytes(done), humanBytes(total))
					setStage(fmt.Sprintf("Downloading %s (%s of %s)", label, humanBytes(done), humanBytes(total)), int(done*100/total))
				} else {
					setStage(fmt.Sprintf("Downloading %s (%s)", label, humanBytes(done)), -1)
					progressf("  %s: %s", label, humanBytes(done))
				}
			}
		}
		if rerr == io.EOF {
			break
		}
		if rerr != nil {
			f.Close()
			os.Remove(part)
			return fmt.Errorf("download %s: %w", label, rerr)
		}
	}
	if err := f.Close(); err != nil {
		os.Remove(part)
		return err
	}
	if total > 0 && done != total {
		os.Remove(part)
		return fmt.Errorf("download %s: incomplete (%d of %d bytes)", label, done, total)
	}
	if err := os.Rename(part, dest); err != nil {
		return err
	}
	logf("  %s: done (%s in %s)", label, humanBytes(done), time.Since(start).Round(time.Second))
	return nil
}

func humanBytes(n int64) string {
	switch {
	case n >= 1<<30:
		return fmt.Sprintf("%.1f GB", float64(n)/(1<<30))
	case n >= 1<<20:
		return fmt.Sprintf("%.1f MB", float64(n)/(1<<20))
	case n >= 1<<10:
		return fmt.Sprintf("%.0f KB", float64(n)/(1<<10))
	}
	return fmt.Sprintf("%d B", n)
}

// extractFilter decides for each archive entry whether to extract it and
// where (relative to destDir). Return "" to skip the entry.
type extractFilter func(name string) string

// stripFirstDir removes the top-level folder from archive paths
// ("Release/whisper-cli.exe" -> "whisper-cli.exe").
func stripFirstDir(name string) string {
	name = strings.TrimPrefix(filepath.ToSlash(name), "./")
	if i := strings.Index(name, "/"); i >= 0 {
		return name[i+1:]
	}
	return name
}

func safeJoin(destDir, rel string) (string, error) {
	p := filepath.Join(destDir, filepath.FromSlash(rel))
	if !strings.HasPrefix(p, filepath.Clean(destDir)+string(os.PathSeparator)) {
		return "", fmt.Errorf("unsafe path in archive: %s", rel)
	}
	return p, nil
}

func extractZip(archive, destDir string, filter extractFilter) (int, error) {
	zr, err := zip.OpenReader(archive)
	if err != nil {
		return 0, err
	}
	defer zr.Close()
	n := 0
	for _, f := range zr.File {
		if f.FileInfo().IsDir() {
			continue
		}
		rel := filter(f.Name)
		if rel == "" {
			continue
		}
		dest, err := safeJoin(destDir, rel)
		if err != nil {
			return n, err
		}
		if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
			return n, err
		}
		rc, err := f.Open()
		if err != nil {
			return n, err
		}
		err = writeFileFrom(dest, rc, f.Mode())
		rc.Close()
		if err != nil {
			return n, err
		}
		n++
	}
	return n, nil
}

func extractTar(archive, destDir string, filter extractFilter) (int, error) {
	f, err := os.Open(archive)
	if err != nil {
		return 0, err
	}
	defer f.Close()
	var r io.Reader = f
	switch {
	case strings.HasSuffix(archive, ".gz"), strings.HasSuffix(archive, ".tgz"):
		gz, err := gzip.NewReader(f)
		if err != nil {
			return 0, err
		}
		defer gz.Close()
		r = gz
	case strings.HasSuffix(archive, ".bz2"):
		r = bzip2.NewReader(f)
	}
	tr := tar.NewReader(r)
	n := 0
	for {
		h, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return n, err
		}
		rel := filter(h.Name)
		if rel == "" {
			continue
		}
		dest, err := safeJoin(destDir, rel)
		if err != nil {
			return n, err
		}
		switch h.Typeflag {
		case tar.TypeReg:
			if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
				return n, err
			}
			if err := writeFileFrom(dest, tr, os.FileMode(h.Mode)); err != nil {
				return n, err
			}
			n++
		case tar.TypeSymlink:
			if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
				return n, err
			}
			os.Remove(dest)
			if err := os.Symlink(h.Linkname, dest); err != nil {
				return n, err
			}
			n++
		}
	}
	return n, nil
}

func writeFileFrom(dest string, r io.Reader, mode os.FileMode) error {
	perm := mode.Perm()
	if perm == 0 {
		perm = 0o644
	}
	if perm&0o100 != 0 {
		perm |= 0o755
	}
	out, err := os.OpenFile(dest, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, perm)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, r); err != nil {
		out.Close()
		return err
	}
	return out.Close()
}

func fileExists(p string) bool {
	st, err := os.Stat(p)
	return err == nil && !st.IsDir()
}
