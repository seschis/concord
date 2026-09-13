package bundle

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

type ScanResult struct {
	Manifest     Manifest
	RepoAbsPaths map[string]string
	FileAbsPaths map[string]string

	rep *Reporter
}

// Reporter receives per-entry callbacks during a scan so callers can report
// live progress. Either field, or the whole Reporter, may be nil.
type Reporter struct {
	OnRepo func(RepoReport)
	OnFile func(FileReport)
}

func (r *Reporter) repo(rep RepoReport) {
	if r != nil && r.OnRepo != nil {
		r.OnRepo(rep)
	}
}

func (r *Reporter) file(rep FileReport) {
	if r != nil && r.OnFile != nil {
		r.OnFile(rep)
	}
}

// RepoReport describes a git repo's disposition, emitted as each repo is
// processed during a scan. Included is true when the repo is carried in the
// bundle (recorded by remote for clone-on-import). Reason is set only when
// Included is false and explains, in a couple of words, why the repo was
// skipped. Note this is distinct from RepoEntry.Bundled, which means the repo's
// tree was packed into the tarball; an included repo is referenced, not packed.
type RepoReport struct {
	RelPath  string
	Included bool
	Reason   string
}

// FileReport describes a non-repo entry (a loose document, or a hidden
// directory that is skipped) as it is processed during a scan. When Skipped is
// true the entry is not packed and Reason explains why in a couple of words.
type FileReport struct {
	RelPath string
	IsDir   bool
	Skipped bool
	Reason  string
}

var skipFiles = map[string]bool{
	".DS_Store": true,
	"Thumbs.db": true,
}

// Scan walks a context directory one level deep and classifies each entry. If
// rep is non-nil its callbacks fire once per repo/file, in filesystem order, as
// the entry is processed, so callers can report progress live.
func Scan(contextDir string, rep *Reporter) (*ScanResult, error) {
	abs, err := filepath.Abs(contextDir)
	if err != nil {
		return nil, fmt.Errorf("resolve context dir: %w", err)
	}
	info, err := os.Stat(abs)
	if err != nil || !info.IsDir() {
		return nil, fmt.Errorf("context dir is not a directory: %s", contextDir)
	}

	res := &ScanResult{
		Manifest: Manifest{
			Version:   ManifestVersion,
			CreatedAt: time.Now().UTC().Format(time.RFC3339),
			SourceDir: abs,
		},
		RepoAbsPaths: make(map[string]string),
		FileAbsPaths: make(map[string]string),
		rep:          rep,
	}

	entries, err := os.ReadDir(abs)
	if err != nil {
		return nil, fmt.Errorf("read context dir: %w", err)
	}

	for _, e := range entries {
		name := e.Name()
		if skipFiles[name] {
			continue
		}
		child := filepath.Join(abs, name)

		if !e.IsDir() {
			res.addFile(name, false)
			res.FileAbsPaths[name] = child
			continue
		}

		if isGitRepo(child) {
			if err := res.addRepo(child, name); err != nil {
				return nil, err
			}
			continue
		}

		if strings.HasPrefix(name, ".") {
			res.skipHiddenDir(name)
			continue
		}

		// Category directory: descend one level.
		if err := res.scanCategory(child, name); err != nil {
			return nil, err
		}
	}

	sort.Slice(res.Manifest.Repos, func(i, j int) bool {
		return res.Manifest.Repos[i].RelPath < res.Manifest.Repos[j].RelPath
	})
	sort.Slice(res.Manifest.Files, func(i, j int) bool {
		return res.Manifest.Files[i].RelPath < res.Manifest.Files[j].RelPath
	})

	return res, nil
}

func (r *ScanResult) scanCategory(catPath, catName string) error {
	entries, err := os.ReadDir(catPath)
	if err != nil {
		return fmt.Errorf("read category dir %s: %w", catName, err)
	}

	for _, e := range entries {
		name := e.Name()
		if skipFiles[name] {
			continue
		}
		rel := filepath.Join(catName, name)
		child := filepath.Join(catPath, name)

		if !e.IsDir() {
			r.addFile(rel, false)
			r.FileAbsPaths[rel] = child
			continue
		}

		if isGitRepo(child) {
			if err := r.addRepo(child, rel); err != nil {
				return err
			}
			continue
		}

		if strings.HasPrefix(name, ".") {
			r.skipHiddenDir(rel)
			continue
		}
	}
	return nil
}

func (r *ScanResult) addRepo(absPath, relPath string) error {
	remote, sha, err := repoInfo(absPath)
	if err != nil {
		return fmt.Errorf("inspect repo %s: %w", relPath, err)
	}
	entry := RepoEntry{
		RelPath: relPath,
		Remote:  remote,
		SHA:     sha,
	}
	// A repo with no remote origin cannot be regenerated on import, so it is
	// skipped rather than packed into the tarball.
	if remote == "" {
		entry.Skipped = true
		entry.SkipReason = "no remote origin"
	}
	r.Manifest.Repos = append(r.Manifest.Repos, entry)
	r.rep.repo(RepoReport{
		RelPath:  relPath,
		Included: !entry.Skipped,
		Reason:   entry.SkipReason,
	})
	return nil
}

func (r *ScanResult) addFile(relPath string, isDir bool) {
	r.Manifest.Files = append(r.Manifest.Files, FileEntry{
		RelPath: relPath,
		IsDir:   isDir,
	})
	r.rep.file(FileReport{RelPath: relPath, IsDir: isDir})
}

// skipHiddenDir reports a hidden directory as skipped without packing it. Hidden
// directories are ignored so caches, VCS metadata, and the like never leak into
// a bundle.
func (r *ScanResult) skipHiddenDir(relPath string) {
	r.rep.file(FileReport{
		RelPath: relPath,
		IsDir:   true,
		Skipped: true,
		Reason:  "hidden dir",
	})
}
