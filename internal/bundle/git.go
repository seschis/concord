package bundle

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
)

func isGitRepo(dir string) bool {
	info, err := os.Stat(filepath.Join(dir, ".git"))
	return err == nil && info.IsDir()
}

func repoInfo(repoPath string) (remote, sha string, err error) {
	remote, err = originRemote(repoPath)
	if err != nil {
		return "", "", err
	}
	sha = gitOutput(repoPath, "rev-parse", "HEAD")
	return remote, sha, nil
}

// originRemote returns the URL of the "origin" remote, or "" if the repo simply
// has no origin configured. A failure to run git at all (missing binary,
// unreadable or corrupt repo) is returned as an error so the caller can abort,
// rather than misreading it as a remote-less repo and silently skipping a repo
// that actually has a remote.
func originRemote(repoPath string) (string, error) {
	cmd := exec.Command("git", "remote")
	cmd.Dir = repoPath
	out, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("git remote in %s: %w", repoPath, gitStderr(err))
	}
	if slices.Contains(strings.Fields(string(out)), "origin") {
		return gitOutput(repoPath, "remote", "get-url", "origin"), nil
	}
	return "", nil
}

// gitStderr enriches an *exec.ExitError with the command's stderr, if any.
func gitStderr(err error) error {
	var ee *exec.ExitError
	if errors.As(err, &ee) && len(ee.Stderr) > 0 {
		return fmt.Errorf("%w: %s", err, strings.TrimSpace(string(ee.Stderr)))
	}
	return err
}

func gitOutput(dir string, args ...string) string {
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	out, err := cmd.Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

func makeCloner(latest, deep bool) Cloner {
	return func(remote, destPath, sha string) error {
		parent := filepath.Dir(destPath)
		if err := os.MkdirAll(parent, 0o755); err != nil {
			return fmt.Errorf("create parent dir: %w", err)
		}

		args := []string{"clone"}
		if !deep {
			args = append(args, "--depth", "1")
		}
		args = append(args, remote, destPath)

		clone := exec.Command("git", args...)
		clone.Dir = parent
		if out, err := clone.CombinedOutput(); err != nil {
			return fmt.Errorf("git clone: %w\n%s", err, out)
		}

		if latest || sha == "" {
			return nil
		}

		head := gitOutput(destPath, "rev-parse", "HEAD")
		if head == sha {
			return nil
		}

		// Try fetching the specific SHA (works on GitHub for repos you can read).
		fetch := exec.Command("git", "-C", destPath, "fetch", "--depth", "1", "origin", sha)
		if fetch.Run() == nil {
			co := exec.Command("git", "-C", destPath, "checkout", sha)
			if co.Run() == nil {
				return nil
			}
		}

		if deep {
			unshallow := exec.Command("git", "-C", destPath, "fetch", "--unshallow")
			if unshallow.Run() == nil {
				co := exec.Command("git", "-C", destPath, "checkout", sha)
				if co.Run() == nil {
					return nil
				}
			}
		}

		return fmt.Errorf("cloned %s but could not check out %s; left at default branch HEAD", remote, sha)
	}
}
