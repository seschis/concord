package bundle

const ManifestVersion = 1

type Manifest struct {
	Version   int         `json:"version"`
	CreatedAt string      `json:"created_at"`
	SourceDir string      `json:"source_dir,omitempty"`
	Repos     []RepoEntry `json:"repos"`
	Files     []FileEntry `json:"files"`
}

type RepoEntry struct {
	RelPath    string `json:"rel_path"`
	Remote     string `json:"remote,omitempty"`
	SHA        string `json:"sha,omitempty"`
	Bundled    bool   `json:"bundled"`
	Skipped    bool   `json:"skipped,omitempty"`
	SkipReason string `json:"skip_reason,omitempty"`
}

type FileEntry struct {
	RelPath string `json:"rel_path"`
	IsDir   bool   `json:"is_dir,omitempty"`
}
