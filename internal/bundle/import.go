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

type Cloner func(remote, destPath, sha string) error

type ImportOptions struct {
	Cloner    Cloner
	Latest    bool
	DeepClone bool
	Reporter  *ImportReporter
}

type ImportResult struct {
	Cloned    []string
	Extracted []string
	Files     []string
	Warnings  []string
}

// ImportStatus is a repo's disposition during import.
type ImportStatus string

const (
	StatusCloned    ImportStatus = "cloned"
	StatusExtracted ImportStatus = "extracted"
	StatusSkipped   ImportStatus = "skipped"
	StatusFailed    ImportStatus = "failed"
)

// ImportReporter receives per-entry callbacks during import so callers can
// report what is being restored live, in manifest order. Either callback, or
// the whole Reporter, may be nil.
type ImportReporter struct {
	OnRepo func(ImportRepoReport)
	OnFile func(ImportFileReport)
}

// ImportRepoReport describes a repo's disposition as import processes it. Reason
// is set for StatusSkipped (why it was skipped at bundle time) and StatusFailed
// (the clone error).
type ImportRepoReport struct {
	RelPath string
	Status  ImportStatus
	Reason  string
}

// ImportFileReport describes a loose file restored from the bundle.
type ImportFileReport struct {
	RelPath string
}

func (r *ImportReporter) repo(rep ImportRepoReport) {
	if r != nil && r.OnRepo != nil {
		r.OnRepo(rep)
	}
}

func (r *ImportReporter) file(rep ImportFileReport) {
	if r != nil && r.OnFile != nil {
		r.OnFile(rep)
	}
}

func Import(bundlePath, destDir string, opts *ImportOptions) (*ImportResult, error) {
	if opts == nil {
		opts = &ImportOptions{}
	}
	cloner := opts.Cloner
	if cloner == nil {
		cloner = makeCloner(opts.Latest, opts.DeepClone)
	}

	abs, err := filepath.Abs(destDir)
	if err != nil {
		return nil, fmt.Errorf("resolve dest dir: %w", err)
	}

	entries, err := os.ReadDir(abs)
	if err == nil && len(entries) > 0 {
		return nil, fmt.Errorf("destination %s already exists and is not empty; remove it first or choose a different path", destDir)
	}

	if err := os.MkdirAll(abs, 0o755); err != nil {
		return nil, fmt.Errorf("create dest dir: %w", err)
	}

	result := &ImportResult{}
	rep := opts.Reporter

	// extractBundle streams a file callback as each loose file is written, so
	// large archives show live progress and only files actually present in the
	// archive are reported.
	manifest, files, err := extractBundle(bundlePath, abs, rep)
	if err != nil {
		return nil, err
	}
	result.Files = files

	for _, re := range manifest.Repos {
		if re.Skipped {
			rep.repo(ImportRepoReport{RelPath: re.RelPath, Status: StatusSkipped, Reason: re.SkipReason})
			continue
		}
		if re.Bundled {
			result.Extracted = append(result.Extracted, re.RelPath)
			rep.repo(ImportRepoReport{RelPath: re.RelPath, Status: StatusExtracted})
			continue
		}

		dest := filepath.Join(abs, re.RelPath)
		if err := cloner(re.Remote, dest, re.SHA); err != nil {
			result.Warnings = append(result.Warnings,
				fmt.Sprintf("%s: %v", re.RelPath, err))
			rep.repo(ImportRepoReport{RelPath: re.RelPath, Status: StatusFailed, Reason: err.Error()})
			continue
		}
		result.Cloned = append(result.Cloned, re.RelPath)
		rep.repo(ImportRepoReport{RelPath: re.RelPath, Status: StatusCloned})
	}

	return result, nil
}

// extractBundle unpacks the archive into destDir and returns the manifest plus
// the relative paths of the loose files actually written. If rep is non-nil its
// OnFile callback fires as each loose file is written, so extraction of a large
// archive reports progress live rather than in one batch afterward.
func extractBundle(bundlePath, destDir string, rep *ImportReporter) (*Manifest, []string, error) {
	f, err := os.Open(bundlePath)
	if err != nil {
		return nil, nil, fmt.Errorf("open bundle: %w", err)
	}
	defer f.Close()

	gz, err := gzip.NewReader(f)
	if err != nil {
		return nil, nil, fmt.Errorf("gzip reader: %w", err)
	}
	defer gz.Close()

	tr := tar.NewReader(gz)

	var manifest *Manifest
	var files []string

	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, nil, fmt.Errorf("read tar: %w", err)
		}

		rel := stripPrefix(hdr.Name)
		if rel == "" {
			continue
		}

		if rel == "manifest.json" {
			data, err := io.ReadAll(tr)
			if err != nil {
				return nil, nil, fmt.Errorf("read manifest: %w", err)
			}
			var m Manifest
			if err := json.Unmarshal(data, &m); err != nil {
				return nil, nil, fmt.Errorf("parse manifest: %w", err)
			}
			manifest = &m
			continue
		}

		var destRel string
		var looseFile bool
		if after, ok := strings.CutPrefix(rel, "files/"); ok {
			destRel = after
			looseFile = true
		} else if after, ok := strings.CutPrefix(rel, "repos/"); ok {
			destRel = after
		} else {
			continue
		}

		if destRel == "" {
			continue
		}

		dest, err := safePath(destDir, destRel)
		if err != nil {
			return nil, nil, err
		}

		switch hdr.Typeflag {
		case tar.TypeDir:
			if err := os.MkdirAll(dest, 0o755); err != nil {
				return nil, nil, fmt.Errorf("mkdir %s: %w", destRel, err)
			}
		case tar.TypeReg:
			if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
				return nil, nil, fmt.Errorf("mkdir parent %s: %w", destRel, err)
			}
			out, err := os.OpenFile(dest, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, os.FileMode(hdr.Mode&0o777))
			if err != nil {
				return nil, nil, fmt.Errorf("create %s: %w", destRel, err)
			}
			if _, err := io.Copy(out, tr); err != nil {
				out.Close()
				return nil, nil, fmt.Errorf("write %s: %w", destRel, err)
			}
			out.Close()

			// Report only after the loose file is fully written, so a caller
			// never sees "restored" for content that was not actually present.
			if looseFile {
				files = append(files, destRel)
				rep.file(ImportFileReport{RelPath: destRel})
			}
		}
	}

	if manifest == nil {
		return nil, nil, fmt.Errorf("bundle does not contain a manifest.json")
	}
	return manifest, files, nil
}

func stripPrefix(name string) string {
	name = filepath.Clean(name)
	prefix := bundlePrefix + string(filepath.Separator)
	if after, ok := strings.CutPrefix(name, prefix); ok {
		return after
	}
	if name == bundlePrefix {
		return ""
	}
	return ""
}
