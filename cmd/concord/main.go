// Command concord is an experimental multi-model security finding triage
// tool: ingest -> context strategy -> quad-model vote -> adjudication -> JSON
// output. Reads and writes local files only.
package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/spf13/cobra"
	"golang.org/x/term"

	"github.com/seschis/concord/internal/agent"
	"github.com/seschis/concord/internal/engine"
	"github.com/seschis/concord/internal/finding"
	"github.com/seschis/concord/internal/ingest"
	iprog "github.com/seschis/concord/internal/progress"
	"github.com/seschis/concord/internal/provider"
	"github.com/seschis/concord/internal/report"
	"github.com/seschis/concord/internal/transcript"
	"github.com/seschis/concord/internal/triage"
	"github.com/seschis/concord/internal/tui"
)

type config struct {
	model           string
	geminiModel     string
	codexModel      string
	azureDeployment string
	azureEndpoint   string
	azureAPIVersion string

	apiKey    string
	googleKey string
	codexKey  string
	azureKey  string

	useBedrock   bool
	bedrockModel string
	awsRegion    string

	noClaude bool
	noGemini bool
	noCodex  bool
	noAzure  bool

	maxTokens       int
	outputDir       string
	contextStrategy string
	srcRoot         string
	contextDirs     []string
	contextGuide    string
	effort          string
	dryRun          bool
	plain           bool
	noTranscripts   bool
	pruneContext    bool
}

// version is set at build time via -ldflags "-X main.version=...".
var version = "dev"

func main() {
	cfg := &config{}
	root := &cobra.Command{
		Use:          "concord [flags] INPUT_FILE",
		Short:        "Experimental multi-model security finding triage",
		Version:      version,
		Args:         cobra.ExactArgs(1),
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			return run(cmd.Context(), cfg, args[0])
		},
	}

	f := root.Flags()
	f.StringVarP(&cfg.model, "model", "m", "claude-opus-5", "Claude model")
	f.StringVar(&cfg.geminiModel, "gemini-model", "gemini-2.5-flash", "Gemini model")
	f.StringVar(&cfg.codexModel, "codex-model", "gpt-5.6-sol", "Codex (OpenAI) model")
	f.StringVar(&cfg.azureDeployment, "azure-deployment", "gpt-5.5", "Azure OpenAI deployment name")
	f.StringVar(&cfg.azureEndpoint, "azure-endpoint", "", "Azure endpoint URL (else AZURE_OPENAI_ENDPOINT)")
	f.StringVar(&cfg.azureAPIVersion, "azure-api-version", "2024-12-01-preview", "Azure API version")

	f.BoolVar(&cfg.useBedrock, "bedrock", false,
		"route Claude through AWS Bedrock (auto-on if CLAUDE_CODE_USE_BEDROCK or AWS_BEARER_TOKEN_BEDROCK is set); "+
			"auth is the Bedrock API key in AWS_BEARER_TOKEN_BEDROCK, else the AWS SigV4 credential chain")
	f.StringVar(&cfg.bedrockModel, "bedrock-model", "us.anthropic.claude-sonnet-4-5-20250929-v1:0",
		"Bedrock model or inference-profile ID for Claude (must be enabled in your account)")
	f.StringVar(&cfg.awsRegion, "aws-region", "", "AWS region for Bedrock (else AWS_REGION / AWS_DEFAULT_REGION)")

	f.StringVar(&cfg.apiKey, "api-key", "", "Anthropic API key (else ANTHROPIC_API_KEY)")
	f.StringVar(&cfg.googleKey, "google-api-key", "", "Google API key (else GOOGLE_API_KEY)")
	f.StringVar(&cfg.codexKey, "codex-api-key", "", "OpenAI API key (else OPENAI_API_KEY)")
	f.StringVar(&cfg.azureKey, "azure-api-key", "", "Azure OpenAI API key (else AZURE_OPENAI_API_KEY)")

	f.BoolVar(&cfg.noClaude, "no-claude", false, "skip Claude")
	f.BoolVar(&cfg.noGemini, "no-gemini", false, "skip Gemini")
	f.BoolVar(&cfg.noCodex, "no-codex", false, "skip Codex")
	f.BoolVar(&cfg.noAzure, "no-azure", false, "skip Azure")

	f.IntVar(&cfg.maxTokens, "max-tokens", 16000, "max tokens per model call")
	f.StringVarP(&cfg.outputDir, "output-dir", "o", "./triage-output", "output directory")
	f.StringVar(&cfg.contextStrategy, "context-strategy", "shared",
		"how voters get code context: shared (one explorer, cheaper) | per-model (each model crawls)")
	f.StringVar(&cfg.srcRoot, "srcroot", "", "source repo root the agents may read")
	f.StringArrayVar(&cfg.contextDirs, "context-dir", nil,
		"extra architecture-context directory the models may read on demand (service repos, docs, network/gateway policy, text diagrams); repeatable; requires --srcroot")
	f.StringVar(&cfg.contextGuide, "context-guide", "",
		"path to a human-written architecture map injected into the brief (where the gateway/services/policies live, using paths relative to a --context-dir root); a TRIAGE_CONTEXT.md at a --context-dir root is picked up automatically")
	f.StringVar(&cfg.effort, "effort", "low", "reasoning effort: low | medium | high")
	f.BoolVar(&cfg.dryRun, "dry-run", false, "parse and normalize findings only, no model calls")
	f.BoolVar(&cfg.plain, "plain", false, "disable the live TUI; print plain per-finding lines (auto on non-TTY output)")
	f.BoolVar(&cfg.noTranscripts, "no-transcripts", false,
		"disable per-finding agentic transcripts (every tool call and result, JSONL) written to <output-dir>/transcripts/; on by default")
	f.BoolVar(&cfg.pruneContext, "enable-context-pruning", false,
		"trim older large tool results from the agentic loop's re-sent context (keeps recent reads, reasoning, and file heads; leaves a re-readable breadcrumb) to cut explorer cost; off by default")

	root.AddCommand(newContextCmd())

	if err := root.Execute(); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}

func run(ctx context.Context, cfg *config, input string) error {
	if cfg.contextStrategy != "shared" && cfg.contextStrategy != "per-model" && cfg.contextStrategy != "quad" {
		return fmt.Errorf("unknown context strategy %q (want shared|per-model)", cfg.contextStrategy)
	}
	if cfg.srcRoot != "" {
		if info, err := os.Stat(cfg.srcRoot); err != nil || !info.IsDir() {
			return fmt.Errorf("srcroot is not a directory: %s", cfg.srcRoot)
		}
	}

	contextRoots, err := buildContextRoots(cfg)
	if err != nil {
		return err
	}
	if cfg.contextGuide != "" {
		if cfg.srcRoot == "" {
			return fmt.Errorf("--context-guide requires --srcroot")
		}
		data, err := os.ReadFile(cfg.contextGuide)
		if err != nil {
			return fmt.Errorf("read context guide: %w", err)
		}
		ctx = triage.WithGuide(ctx, string(data))
	}

	if cfg.pruneContext {
		ctx = agent.WithPruner(ctx, agent.NewBreadcrumbPruner())
	}

	fmt.Printf("Loading findings from: %s\n", input)
	findings, err := ingest.Load(input)
	if err != nil {
		return err
	}
	if len(findings) == 0 {
		fmt.Println("No findings found.")
		return nil
	}
	fmt.Printf("Loaded %d findings\n", len(findings))

	if cfg.dryRun {
		path, err := report.WriteFindings(cfg.outputDir, findings)
		if err != nil {
			return err
		}
		for _, fnd := range findings {
			fmt.Printf("  %s  %s  (%s)  %s:%d\n", fnd.ID, fnd.VulnType, fnd.Severity, fnd.File, fnd.Line)
		}
		fmt.Printf("\nDry run complete. Normalized findings written to: %s\n", path)
		return nil
	}

	voters, claude, names := resolveProviders(ctx, cfg)
	if len(voters) == 0 {
		return fmt.Errorf("no models available; set credentials for at least one of Claude, Gemini, Codex, or Azure")
	}

	// Use the live TUI on an interactive terminal unless --plain is set; a
	// non-TTY stdout (pipe, redirect, CI) always gets plain line output.
	useTUI := !cfg.plain && term.IsTerminal(int(os.Stdout.Fd()))

	// The gatherer (shared strategy) and adjudicator prefer Claude, else the
	// first available voter. Every provider is an *LLMProvider, so both roles
	// are satisfied.
	var gatherer engine.ContextGatherer = claude
	var adjudicator engine.Adjudicator = claude
	if claude == nil {
		gatherer = voters[0].(engine.ContextGatherer)
		adjudicator = voters[0].(engine.Adjudicator)
	}

	strategy, err := engine.NewStrategy(cfg.contextStrategy, gatherer)
	if err != nil {
		return err
	}

	if !useTUI {
		fmt.Printf("Active models: %s\n", strings.Join(names, ", "))
		switch {
		case cfg.srcRoot == "":
			fmt.Println("Context: none (metadata-only). Pass --srcroot to enable code reading.")
		case strategy.Name() == "per-model":
			fmt.Printf("Context: per-model agents reading %s (effort=%s)\n", cfg.srcRoot, cfg.effort)
		default:
			fmt.Printf("Context: shared explorer over %s (effort=%s)\n", cfg.srcRoot, cfg.effort)
		}
		if len(contextRoots) > 0 {
			fmt.Printf("Architecture context: %s\n", strings.Join(contextRootLabels(contextRoots), ", "))
		}
	}

	if !cfg.noTranscripts {
		transcriptDir := filepath.Join(cfg.outputDir, "transcripts")
		ctx = transcript.WithWriter(ctx, transcript.NewFileWriter(transcriptDir))
		if !useTUI {
			fmt.Printf("Transcripts: %s\n", transcriptDir)
		}
	}

	eng := &engine.Engine{
		Voters:       voters,
		Strategy:     strategy,
		Adjudicator:  adjudicator,
		SrcRoot:      cfg.srcRoot,
		ContextRoots: contextRoots,
		Effort:       cfg.effort,
	}

	var results []report.FindingResult
	var cost float64

	if useTUI {
		hdr := tui.Header{
			InputFile: input, Models: names, Strategy: strategy.Name(),
			SrcRoot: cfg.srcRoot, Effort: cfg.effort,
			Context: contextRootLabels(contextRoots),
		}
		outcome := tui.Run(ctx, hdr, func(rctx context.Context, sink iprog.Sink) error {
			results, cost = eng.TriageAll(iprog.WithSink(rctx, sink), findings, nil)
			return rctx.Err()
		})
		if outcome.Err != nil {
			fmt.Println("Run interrupted; no report written.")
			return nil
		}
		if !outcome.WriteReport {
			fmt.Println("Aborted; no report written.")
			return nil
		}
		if outcome.Partial {
			fmt.Printf("\nStopped early: writing a partial report for %d of %d findings.\n", len(results), len(findings))
		}
	} else {
		total := len(findings)
		plainProgress := func(i int, f finding.Finding, results []triage.Result, final triage.Verdict, agreement string, adjudicated bool) {
			note := agreement
			if adjudicated {
				note = "adjudicated"
			}
			fmt.Printf("  [%d/%d] %s  %s  ->  %s (%s)\n", i+1, total, f.ID, f.VulnType, final, note)
		}
		results, cost = eng.TriageAll(ctx, findings, plainProgress)
	}

	meta := report.Meta{
		Date:            time.Now().Format("2006-01-02 15:04:05"),
		InputFile:       input,
		SrcRoot:         cfg.srcRoot,
		ContextStrategy: strategy.Name(),
		ContextDirs:     cfg.contextDirs,
		ContextGuides:   discoveredGuides(cfg, contextRoots),
		Effort:          cfg.effort,
		Models:          names,
		Total:           len(findings),
		TotalCostUSD:    cost,
	}
	jsonPath, err := report.WriteJSON(cfg.outputDir, meta, results)
	if err != nil {
		return err
	}
	mdPath, err := report.WriteMarkdown(cfg.outputDir, meta, results)
	if err != nil {
		return err
	}

	fmt.Printf("\nTriage complete.  Total cost: $%.4f\n", cost)
	fmt.Printf("Results written to:\n  %s\n  %s\n", jsonPath, mdPath)
	return nil
}

// resolveProviders constructs every enabled provider whose credentials are
// present, skipping the rest with a message. It returns the voters, the Claude
// provider (nil if unavailable), and the display names.
func resolveProviders(ctx context.Context, cfg *config) ([]provider.Provider, *provider.LLMProvider, []string) {
	var voters []provider.Provider
	var names []string
	var claude *provider.LLMProvider

	add := func(p *provider.LLMProvider, label string, err error) {
		if err != nil {
			fmt.Printf("  skipping %s: %s\n", label, short(err.Error()))
			return
		}
		voters = append(voters, p)
		names = append(names, fmt.Sprintf("%s (%s)", label, p.Model()))
	}

	if !cfg.noClaude {
		var p *provider.LLMProvider
		var err error
		label := "Claude"
		if cfg.useBedrock || envTruthy("CLAUDE_CODE_USE_BEDROCK") || os.Getenv("AWS_BEARER_TOKEN_BEDROCK") != "" {
			region := firstNonEmpty(cfg.awsRegion, os.Getenv("AWS_REGION"), os.Getenv("AWS_DEFAULT_REGION"))
			p, err = provider.NewClaudeBedrock(ctx, cfg.bedrockModel, region, cfg.maxTokens)
			label = "Claude (Bedrock)"
		} else {
			p, err = provider.NewClaude(cfg.apiKey, cfg.model, cfg.maxTokens)
		}
		if err == nil {
			claude = p
		}
		add(p, label, err)
	}
	if !cfg.noGemini {
		p, err := provider.NewGemini(ctx, cfg.googleKey, cfg.geminiModel, cfg.maxTokens)
		add(p, "Gemini", err)
	}
	if !cfg.noCodex {
		p, err := provider.NewCodex(cfg.codexKey, cfg.codexModel, cfg.maxTokens)
		add(p, "Codex", err)
	}
	if !cfg.noAzure {
		key := firstNonEmpty(cfg.azureKey, os.Getenv("AZURE_OPENAI_API_KEY"))
		endpoint := firstNonEmpty(cfg.azureEndpoint, os.Getenv("AZURE_OPENAI_ENDPOINT"))
		p, err := provider.NewAzure(key, endpoint, cfg.azureAPIVersion, cfg.azureDeployment, cfg.maxTokens)
		add(p, "Azure", err)
	}
	return voters, claude, names
}

// buildContextRoots validates each --context-dir and turns it into a labeled
// agent.Root (label = directory basename). Context dirs require --srcroot, since
// they augment the finding repo the tools are rooted at.
func buildContextRoots(cfg *config) ([]agent.Root, error) {
	if len(cfg.contextDirs) == 0 {
		return nil, nil
	}
	if cfg.srcRoot == "" {
		return nil, fmt.Errorf("--context-dir requires --srcroot (context augments the finding's repository)")
	}
	roots := make([]agent.Root, 0, len(cfg.contextDirs))
	for _, d := range cfg.contextDirs {
		info, err := os.Stat(d)
		if err != nil || !info.IsDir() {
			return nil, fmt.Errorf("context dir is not a directory: %s", d)
		}
		roots = append(roots, agent.Root{Label: filepath.Base(filepath.Clean(d)), Path: d})
	}
	return roots, nil
}

// discoveredGuides reports, for the run's Meta, which architecture guides were
// actually available at start time: the explicit --context-guide (if any) plus
// any ConventionGuideFile found at a context root's top level. This mirrors
// agent.ToolBox.collectGuides's discovery rules so the report can answer "did a
// guide even exist for this run" without inspecting file timestamps after the
// fact.
func discoveredGuides(cfg *config, contextRoots []agent.Root) []string {
	var guides []string
	if cfg.contextGuide != "" {
		guides = append(guides, fmt.Sprintf("--context-guide: %s", cfg.contextGuide))
	}
	for _, r := range contextRoots {
		p := filepath.Join(r.Path, agent.ConventionGuideFile)
		if info, err := os.Stat(p); err == nil && !info.IsDir() {
			guides = append(guides, fmt.Sprintf("%s (root: %s)", agent.ConventionGuideFile, r.Label))
		}
	}
	return guides
}

func contextRootLabels(roots []agent.Root) []string {
	out := make([]string, len(roots))
	for i, r := range roots {
		out[i] = r.Label
	}
	return out
}

// envTruthy reports whether an env var is set to a non-empty, non-"0"/"false"
// value, matching how CLAUDE_CODE_USE_BEDROCK is conventionally toggled.
func envTruthy(name string) bool {
	v := strings.ToLower(strings.TrimSpace(os.Getenv(name)))
	return v != "" && v != "0" && v != "false" && v != "no"
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}

func short(s string) string {
	if len(s) > 100 {
		return s[:100]
	}
	return s
}
