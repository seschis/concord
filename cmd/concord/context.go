package main

import (
	"fmt"
	"path/filepath"

	"github.com/charmbracelet/lipgloss"
	"github.com/spf13/cobra"

	"github.com/seschis/concord/internal/bundle"
)

// Status colors for export output. lipgloss auto-degrades to no color when
// stdout is not a terminal (and honors NO_COLOR), matching the rest of the CLI.
var (
	bundledStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("42"))  // repos, green
	fileStyle    = lipgloss.NewStyle().Foreground(lipgloss.Color("44"))  // files, cyan
	skippedStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("214")) // amber, warning
)

func newContextCmd() *cobra.Command {
	ctx := &cobra.Command{
		Use:   "context",
		Short: "Manage architecture-context bundles (export/import)",
	}
	ctx.AddCommand(newExportCmd(), newImportCmd())
	return ctx
}

func newExportCmd() *cobra.Command {
	var (
		contextDir string
		output     string
	)

	cmd := &cobra.Command{
		Use:   "export",
		Short: "Export a context directory to a portable .tar.gz bundle",
		Long: `Scan a --context-dir directory, record every git repo's remote URL and
commit SHA in a JSON manifest, and write a .tar.gz bundle. Loose files are
packed directly and repos with a remote are regenerated on import via git
clone. Git repos without a remote origin are skipped, since they cannot be
re-cloned. Each repo found is listed with its disposition as it is processed.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			if contextDir == "" {
				return fmt.Errorf("--context-dir is required")
			}

			abs, err := filepath.Abs(output)
			if err != nil {
				return err
			}

			fmt.Printf("Scanning %s ...\n", contextDir)
			var skippedDirs int
			rep := &bundle.Reporter{
				OnRepo: func(r bundle.RepoReport) {
					if r.Included {
						fmt.Printf("  repo %s ... %s\n", r.RelPath, bundledStyle.Render("bundled"))
					} else {
						fmt.Printf("  repo %s ... %s\n", r.RelPath, skippedStyle.Render("skipped ("+r.Reason+")"))
					}
				},
				OnFile: func(f bundle.FileReport) {
					kind := "file"
					path := f.RelPath
					if f.IsDir {
						kind = "dir"
						path += "/"
					}
					if f.Skipped {
						skippedDirs++
						fmt.Printf("  %s %s ... %s\n", kind, path, skippedStyle.Render("skipped ("+f.Reason+")"))
					} else {
						fmt.Printf("  %s %s ... %s\n", kind, path, fileStyle.Render("bundled"))
					}
				},
			}
			manifest, err := bundle.Export(contextDir, abs, rep)
			if err != nil {
				return err
			}

			var bundledCount, skippedRepos int
			for _, r := range manifest.Repos {
				if r.Skipped {
					skippedRepos++
				} else {
					bundledCount++
				}
			}

			fmt.Printf("Bundle written to %s\n", abs)
			fmt.Printf("  %d repos bundled, %d repos skipped, %d files, %d hidden dirs skipped\n",
				bundledCount, skippedRepos, len(manifest.Files), skippedDirs)
			return nil
		},
	}

	cmd.Flags().StringVar(&contextDir, "context-dir", "", "path to the context directory to export (required)")
	cmd.Flags().StringVarP(&output, "output", "o", "context-bundle.tar.gz", "output bundle path")
	return cmd
}

func newImportCmd() *cobra.Command {
	var (
		output    string
		latest    bool
		deepClone bool
	)

	cmd := &cobra.Command{
		Use:   "import BUNDLE",
		Short: "Import a context bundle, cloning repos and restoring files",
		Long: `Extract a .tar.gz context bundle into a directory. Remote repos listed in
the manifest are cloned via git; repos that fail (permissions, network) produce
a warning but do not abort the import. Bundled repos and loose files are
extracted directly.

By default repos are shallow-cloned and checked out at the exact commit recorded
in the manifest. Pass --latest to clone the current default branch HEAD instead.
Pass --deep-clone to fetch full history.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if output == "" {
				return fmt.Errorf("-o/--output is required")
			}

			bundlePath := args[0]
			if latest {
				fmt.Printf("Importing %s into %s (cloning at latest HEAD) ...\n", bundlePath, output)
			} else {
				fmt.Printf("Importing %s into %s (pinned to manifest refs) ...\n", bundlePath, output)
			}

			importRep := &bundle.ImportReporter{
				OnFile: func(f bundle.ImportFileReport) {
					fmt.Printf("  file %s ... %s\n", f.RelPath, fileStyle.Render("restored"))
				},
				OnRepo: func(r bundle.ImportRepoReport) {
					switch r.Status {
					case bundle.StatusCloned, bundle.StatusExtracted:
						fmt.Printf("  repo %s ... %s\n", r.RelPath, bundledStyle.Render(string(r.Status)))
					case bundle.StatusSkipped:
						fmt.Printf("  repo %s ... %s\n", r.RelPath, skippedStyle.Render("skipped ("+r.Reason+")"))
					default: // failed
						fmt.Printf("  repo %s ... %s\n", r.RelPath, skippedStyle.Render("failed"))
					}
				},
			}

			result, err := bundle.Import(bundlePath, output, &bundle.ImportOptions{
				Latest:    latest,
				DeepClone: deepClone,
				Reporter:  importRep,
			})
			if err != nil {
				return err
			}

			fmt.Printf("  Cloned:    %d repos\n", len(result.Cloned))
			fmt.Printf("  Extracted: %d bundled repos\n", len(result.Extracted))
			fmt.Printf("  Files:     %d\n", len(result.Files))

			if len(result.Warnings) > 0 {
				fmt.Printf("\nWarnings (%d repos could not be cloned):\n", len(result.Warnings))
				for _, w := range result.Warnings {
					fmt.Printf("  - %s\n", w)
				}
			}

			fmt.Println("\nDone. Use --context-dir", output, "when running concord.")
			return nil
		},
	}

	cmd.Flags().StringVarP(&output, "output", "o", "", "destination directory (required)")
	cmd.Flags().BoolVar(&latest, "latest", false, "clone repos at current default branch HEAD instead of the manifest's pinned ref")
	cmd.Flags().BoolVar(&deepClone, "deep-clone", false, "fetch full git history instead of shallow clones")
	return cmd
}
