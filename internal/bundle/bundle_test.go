package bundle

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func TestScan(t *testing.T) {
	dir := setupContextDir(t)

	var reports []RepoReport
	var fileReports []FileReport
	res, err := Scan(dir, &Reporter{
		OnRepo: func(r RepoReport) { reports = append(reports, r) },
		OnFile: func(f FileReport) { fileReports = append(fileReports, f) },
	})
	if err != nil {
		t.Fatalf("Scan: %v", err)
	}

	// Every repo found should have been reported as it was processed.
	if len(reports) != len(res.Manifest.Repos) {
		t.Errorf("got %d repo reports, want %d", len(reports), len(res.Manifest.Repos))
	}

	// Every bundled non-repo entry should have been reported. Skipped entries
	// (hidden dirs) are reported but not added to the manifest.
	var bundledFiles int
	var sawGuide, sawHidden bool
	for _, f := range fileReports {
		if f.Skipped {
			continue
		}
		bundledFiles++
		if f.RelPath == "TRIAGE_CONTEXT.md" {
			sawGuide = true
		}
	}
	if bundledFiles != len(res.Manifest.Files) {
		t.Errorf("got %d bundled file reports, want %d", bundledFiles, len(res.Manifest.Files))
	}
	if !sawGuide {
		t.Error("expected TRIAGE_CONTEXT.md to be reported as a bundled file")
	}

	// The hidden directory must be reported skipped and never appear in the
	// manifest's file list.
	for _, f := range fileReports {
		if f.RelPath == ".cache" {
			sawHidden = true
			if !f.Skipped || f.Reason == "" {
				t.Errorf(".cache report = %+v, want skipped with reason", f)
			}
		}
	}
	if !sawHidden {
		t.Error("expected hidden dir .cache to be reported")
	}
	for _, f := range res.Manifest.Files {
		if f.RelPath == ".cache" {
			t.Error(".cache should not be in the manifest (hidden dir)")
		}
	}
	reportByPath := make(map[string]RepoReport)
	for _, r := range reports {
		reportByPath[r.RelPath] = r
	}
	if r := reportByPath[filepath.Join("team-a", "no-remote")]; r.Included || r.Reason == "" {
		t.Errorf("no-remote report = %+v, want skipped with reason", r)
	}
	if r := reportByPath[filepath.Join("team-a", "has-remote")]; !r.Included {
		t.Errorf("has-remote report = %+v, want included", r)
	}

	if res.Manifest.Version != ManifestVersion {
		t.Errorf("version = %d, want %d", res.Manifest.Version, ManifestVersion)
	}

	reposByPath := make(map[string]RepoEntry)
	for _, r := range res.Manifest.Repos {
		reposByPath[r.RelPath] = r
	}

	// Remote repo should not be bundled.
	if r, ok := reposByPath[filepath.Join("team-a", "has-remote")]; !ok {
		t.Error("missing repo team-a/has-remote")
	} else {
		if r.Bundled {
			t.Error("has-remote should not be bundled")
		}
		if r.Remote == "" {
			t.Error("has-remote should have a remote URL")
		}
		if r.SHA == "" {
			t.Error("has-remote should have a SHA")
		}
	}

	// Local-only repo should be skipped, not bundled.
	if r, ok := reposByPath[filepath.Join("team-a", "no-remote")]; !ok {
		t.Error("missing repo team-a/no-remote")
	} else {
		if !r.Skipped {
			t.Error("no-remote should be skipped")
		}
		if r.SkipReason == "" {
			t.Error("no-remote should have a skip reason")
		}
		if r.Remote != "" {
			t.Errorf("no-remote remote = %q, want empty", r.Remote)
		}
	}

	filesByPath := make(map[string]FileEntry)
	for _, f := range res.Manifest.Files {
		filesByPath[f.RelPath] = f
	}

	if _, ok := filesByPath["TRIAGE_CONTEXT.md"]; !ok {
		t.Error("missing file TRIAGE_CONTEXT.md")
	}
}

// A directory that looks like a repo (has a .git dir) but is not a valid git
// repo must abort the scan, not be silently treated as remote-less and skipped.
func TestScanAbortsOnBrokenRepo(t *testing.T) {
	dir := t.TempDir()
	broken := filepath.Join(dir, "team-a", "broken")
	if err := os.MkdirAll(filepath.Join(broken, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}

	_, err := Scan(dir, nil)
	if err == nil {
		t.Fatal("expected Scan to fail on a repo git cannot inspect, got nil")
	}
}

func TestExportImportRoundTrip(t *testing.T) {
	srcDir := setupContextDir(t)
	bundlePath := filepath.Join(t.TempDir(), "test.tar.gz")

	manifest, err := Export(srcDir, bundlePath, nil)
	if err != nil {
		t.Fatalf("Export: %v", err)
	}
	if len(manifest.Repos) == 0 {
		t.Fatal("manifest has no repos")
	}

	destDir := filepath.Join(t.TempDir(), "imported")

	// Use a local cloner that clones from the source dir instead of a remote.
	localCloner := func(remote, destPath, sha string) error {
		reposByRemote := map[string]string{}
		for _, r := range manifest.Repos {
			if r.Remote != "" {
				absPath := filepath.Join(srcDir, r.RelPath)
				reposByRemote[r.Remote] = absPath
			}
		}
		localPath, ok := reposByRemote[remote]
		if !ok {
			return fmt.Errorf("unknown remote: %s", remote)
		}
		if err := os.MkdirAll(filepath.Dir(destPath), 0o755); err != nil {
			return err
		}
		cmd := exec.Command("git", "clone", localPath, destPath)
		return cmd.Run()
	}

	var repoReports []ImportRepoReport
	var fileReports []ImportFileReport
	result, err := Import(bundlePath, destDir, &ImportOptions{
		Cloner: localCloner,
		Reporter: &ImportReporter{
			OnRepo: func(r ImportRepoReport) { repoReports = append(repoReports, r) },
			OnFile: func(f ImportFileReport) { fileReports = append(fileReports, f) },
		},
	})
	if err != nil {
		t.Fatalf("Import: %v", err)
	}

	if len(result.Warnings) > 0 {
		t.Errorf("unexpected warnings: %v", result.Warnings)
	}

	// The reporter should have streamed a status for every repo and file.
	statusByRepo := make(map[string]ImportStatus)
	for _, r := range repoReports {
		statusByRepo[r.RelPath] = r.Status
	}
	if s := statusByRepo[filepath.Join("team-a", "has-remote")]; s != StatusCloned {
		t.Errorf("has-remote import status = %q, want %q", s, StatusCloned)
	}
	if s := statusByRepo[filepath.Join("team-a", "no-remote")]; s != StatusSkipped {
		t.Errorf("no-remote import status = %q, want %q", s, StatusSkipped)
	}
	if len(fileReports) != len(result.Files) {
		t.Errorf("got %d file reports, want %d", len(fileReports), len(result.Files))
	}

	// The cloned remote repo should exist.
	clonedPath := filepath.Join(destDir, "team-a", "has-remote")
	if !isGitRepo(clonedPath) {
		t.Error("cloned repo team-a/has-remote is not a git repo")
	}

	// The local-only repo has no remote origin and should have been skipped.
	skippedPath := filepath.Join(destDir, "team-a", "no-remote")
	if _, err := os.Stat(skippedPath); !os.IsNotExist(err) {
		t.Error("no-remote repo should have been skipped, not present in import")
	}

	// The hidden directory should never have been bundled.
	if _, err := os.Stat(filepath.Join(destDir, ".cache")); !os.IsNotExist(err) {
		t.Error("hidden dir .cache should have been skipped, not present in import")
	}

	// The loose file should exist.
	guidePath := filepath.Join(destDir, "TRIAGE_CONTEXT.md")
	data, err := os.ReadFile(guidePath)
	if err != nil {
		t.Fatalf("read TRIAGE_CONTEXT.md: %v", err)
	}
	if string(data) != "test guide" {
		t.Errorf("TRIAGE_CONTEXT.md = %q, want %q", data, "test guide")
	}
}

func TestImportPartialFailure(t *testing.T) {
	srcDir := setupContextDir(t)
	bundlePath := filepath.Join(t.TempDir(), "test.tar.gz")

	if _, err := Export(srcDir, bundlePath, nil); err != nil {
		t.Fatalf("Export: %v", err)
	}

	destDir := filepath.Join(t.TempDir(), "imported")

	failCloner := func(remote, destPath, sha string) error {
		return fmt.Errorf("permission denied")
	}

	var failed []string
	result, err := Import(bundlePath, destDir, &ImportOptions{
		Cloner: failCloner,
		Reporter: &ImportReporter{
			OnRepo: func(r ImportRepoReport) {
				if r.Status == StatusFailed {
					failed = append(failed, r.RelPath)
				}
			},
		},
	})
	if err != nil {
		t.Fatalf("Import: %v", err)
	}

	if len(result.Warnings) == 0 {
		t.Error("expected warnings for failed clones")
	}
	if len(failed) != len(result.Warnings) {
		t.Errorf("reported %d failed repos, want %d (one per warning)", len(failed), len(result.Warnings))
	}
	if len(result.Cloned) != 0 {
		t.Errorf("expected 0 cloned, got %d", len(result.Cloned))
	}

	// No-remote repos are skipped, so nothing is extracted from the tarball.
	if len(result.Extracted) != 0 {
		t.Errorf("expected 0 extracted repos, got %d", len(result.Extracted))
	}

	// Loose files should still be present.
	guidePath := filepath.Join(destDir, "TRIAGE_CONTEXT.md")
	if _, err := os.Stat(guidePath); err != nil {
		t.Error("TRIAGE_CONTEXT.md should exist even when clones fail")
	}
}

func TestImportRefusesNonEmptyDest(t *testing.T) {
	srcDir := setupContextDir(t)
	bundlePath := filepath.Join(t.TempDir(), "test.tar.gz")

	if _, err := Export(srcDir, bundlePath, nil); err != nil {
		t.Fatalf("Export: %v", err)
	}

	destDir := t.TempDir()
	os.WriteFile(filepath.Join(destDir, "existing.txt"), []byte("x"), 0o644)

	_, err := Import(bundlePath, destDir, nil)
	if err == nil {
		t.Error("expected error importing into non-empty dir")
	}
}

func TestManifestJSON(t *testing.T) {
	m := Manifest{
		Version:   1,
		CreatedAt: "2026-01-01T00:00:00Z",
		Repos: []RepoEntry{
			{RelPath: "a/b", Remote: "https://example.com/b.git", SHA: "abc123", Bundled: false},
			{RelPath: "a/c", Bundled: true},
		},
		Files: []FileEntry{
			{RelPath: "guide.md"},
		},
	}

	data, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		t.Fatal(err)
	}

	var m2 Manifest
	if err := json.Unmarshal(data, &m2); err != nil {
		t.Fatal(err)
	}

	if m2.Version != 1 {
		t.Errorf("version = %d, want 1", m2.Version)
	}
	if len(m2.Repos) != 2 {
		t.Fatalf("repos len = %d, want 2", len(m2.Repos))
	}
	if m2.Repos[0].Remote != "https://example.com/b.git" {
		t.Errorf("repos[0].Remote = %q", m2.Repos[0].Remote)
	}
	if !m2.Repos[1].Bundled {
		t.Error("repos[1] should be bundled")
	}
}

func TestSafePathRejectsTraversal(t *testing.T) {
	base := "/dest"
	for _, bad := range []string{"../etc/passwd", "/abs/path", "foo/../../etc"} {
		if _, err := safePath(base, bad); err == nil {
			t.Errorf("safePath(%q, %q) should have failed", base, bad)
		}
	}

	good, err := safePath(base, "a/b/c.txt")
	if err != nil {
		t.Errorf("safePath(base, 'a/b/c.txt') failed: %v", err)
	}
	if good != filepath.Join(base, "a", "b", "c.txt") {
		t.Errorf("safePath = %q, want %q", good, filepath.Join(base, "a", "b", "c.txt"))
	}
}

// setupContextDir creates a temp directory that mimics a real context dir
// with a top-level guide, a category dir containing a repo with a remote and
// a repo without a remote.
func setupContextDir(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()

	// Top-level guide file.
	os.WriteFile(filepath.Join(dir, "TRIAGE_CONTEXT.md"), []byte("test guide"), 0o644)

	// Hidden directory that must be skipped, not bundled.
	hiddenDir := filepath.Join(dir, ".cache")
	os.MkdirAll(hiddenDir, 0o755)
	os.WriteFile(filepath.Join(hiddenDir, "junk.txt"), []byte("cache"), 0o644)

	// Category: team-a
	teamDir := filepath.Join(dir, "team-a")
	os.MkdirAll(teamDir, 0o755)

	// Repo with a remote.
	withRemote := filepath.Join(teamDir, "has-remote")
	gitInit(t, withRemote)
	gitExec(t, withRemote, "remote", "add", "origin", "https://github.com/example/has-remote.git")
	os.WriteFile(filepath.Join(withRemote, "main.go"), []byte("package main"), 0o644)
	gitExec(t, withRemote, "add", ".")
	gitExec(t, withRemote, "commit", "-m", "init")

	// Repo without a remote.
	noRemote := filepath.Join(teamDir, "no-remote")
	gitInit(t, noRemote)
	os.WriteFile(filepath.Join(noRemote, "local.txt"), []byte("local only"), 0o644)
	gitExec(t, noRemote, "add", ".")
	gitExec(t, noRemote, "commit", "-m", "init")

	return dir
}

func gitInit(t *testing.T, dir string) {
	t.Helper()
	os.MkdirAll(dir, 0o755)
	gitExec(t, dir, "init")
	gitExec(t, dir, "config", "user.email", "test@test.com")
	gitExec(t, dir, "config", "user.name", "Test")
}

func gitExec(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v in %s: %v\n%s", args, dir, err, out)
	}
}
