package bundle

import (
	"archive/tar"
	"compress/gzip"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

const bundlePrefix = "context-bundle"

// Export scans contextDir and writes a .tar.gz bundle to outPath. If rep is
// non-nil its callbacks fire once per repo and per file as the scan processes
// them, reporting what is being bundled (or skipped, and why).
func Export(contextDir, outPath string, rep *Reporter) (*Manifest, error) {
	scan, err := Scan(contextDir, rep)
	if err != nil {
		return nil, err
	}

	f, err := os.Create(outPath)
	if err != nil {
		return nil, fmt.Errorf("create bundle: %w", err)
	}
	defer f.Close()

	gz := gzip.NewWriter(f)
	defer gz.Close()

	tw := tar.NewWriter(gz)
	defer tw.Close()

	manifestJSON, err := json.MarshalIndent(scan.Manifest, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("marshal manifest: %w", err)
	}
	if err := writeEntry(tw, filepath.Join(bundlePrefix, "manifest.json"), manifestJSON); err != nil {
		return nil, err
	}

	for _, fe := range scan.Manifest.Files {
		absPath := scan.FileAbsPaths[fe.RelPath]
		tarDir := filepath.Join(bundlePrefix, "files", fe.RelPath)
		if fe.IsDir {
			if err := addTree(tw, absPath, tarDir); err != nil {
				return nil, fmt.Errorf("bundle file dir %s: %w", fe.RelPath, err)
			}
		} else {
			data, err := os.ReadFile(absPath)
			if err != nil {
				return nil, fmt.Errorf("read file %s: %w", fe.RelPath, err)
			}
			if err := writeEntry(tw, tarDir, data); err != nil {
				return nil, err
			}
		}
	}

	for _, re := range scan.Manifest.Repos {
		if re.Skipped || !re.Bundled {
			continue
		}
		absPath := scan.RepoAbsPaths[re.RelPath]
		tarDir := filepath.Join(bundlePrefix, "repos", re.RelPath)
		if err := addTree(tw, absPath, tarDir); err != nil {
			return nil, fmt.Errorf("bundle repo %s: %w", re.RelPath, err)
		}
	}

	return &scan.Manifest, nil
}

func writeEntry(tw *tar.Writer, name string, data []byte) error {
	hdr := &tar.Header{
		Name: name,
		Mode: 0o644,
		Size: int64(len(data)),
	}
	if err := tw.WriteHeader(hdr); err != nil {
		return fmt.Errorf("write tar header %s: %w", name, err)
	}
	if _, err := tw.Write(data); err != nil {
		return fmt.Errorf("write tar data %s: %w", name, err)
	}
	return nil
}

func addTree(tw *tar.Writer, root, tarPrefix string) error {
	return filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if skipFiles[info.Name()] {
			return nil
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		tarPath := filepath.Join(tarPrefix, rel)

		if info.IsDir() {
			hdr := &tar.Header{
				Typeflag: tar.TypeDir,
				Name:     tarPath + "/",
				Mode:     0o755,
			}
			return tw.WriteHeader(hdr)
		}

		if !info.Mode().IsRegular() {
			return nil
		}

		f, err := os.Open(path)
		if err != nil {
			// Unreadable files (e.g. mode 000) are preserved as zero-byte
			// entries so the import recreates them with the right permissions.
			hdr := &tar.Header{
				Name: tarPath,
				Mode: int64(info.Mode().Perm()),
				Size: 0,
			}
			return tw.WriteHeader(hdr)
		}
		defer f.Close()

		hdr := &tar.Header{
			Name: tarPath,
			Mode: int64(info.Mode().Perm()),
			Size: info.Size(),
		}
		if err := tw.WriteHeader(hdr); err != nil {
			return err
		}

		_, err = io.Copy(tw, f)
		return err
	})
}

func safePath(base, entry string) (string, error) {
	cleaned := filepath.Clean(entry)
	if filepath.IsAbs(cleaned) || strings.HasPrefix(cleaned, "..") {
		return "", fmt.Errorf("unsafe tar entry path: %s", entry)
	}
	joined := filepath.Join(base, cleaned)
	if !strings.HasPrefix(joined, base+string(filepath.Separator)) && joined != base {
		return "", fmt.Errorf("path traversal in tar entry: %s", entry)
	}
	return joined, nil
}
