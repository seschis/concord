// Command harmonia is an experimental multi-model security finding triage
// tool: ingest -> context strategy -> multi-model vote -> judge-panel
// adjudication of ties -> JSON output. Reads and writes local files only.
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

	"github.com/seschis/harmonia/internal/agent"
	"github.com/seschis/harmonia/internal/engine"
	"github.com/seschis/harmonia/internal/finding"
	"github.com/seschis/harmonia/internal/ingest"
	iprog "github.com/seschis/harmonia/internal/progress"
	"github.com/seschis/harmonia/internal/provider"
	"github.com/seschis/harmonia/internal/report"
	"github.com/seschis/harmonia/internal/transcript"
	"github.com/seschis/harmonia/internal/triage"
	"github.com/seschis/harmonia/internal/tui"
)

type config struct {
	// Model config layers: harmonia.toml (discovered or --config), one-shot
	// --add-model specs, and per-name skips. Precedence per field: flag > file
	// > preset.
	configFile string
	addModels  []string // --add-model "name,key=value,..." (repeatable)
	noModels   []string // --no-model NAME (repeatable)

	model           string
	geminiModel     string
	openaiModel     string
	azureDeployment string
	azureEndpoint   string
	azureAPIVersion string
	claudeEndpoint  string
	openaiEndpoint  string

	apiKey    string
	googleKey string
	openaiKey string
	azureKey  string

	useBedrock   bool
	bedrockModel string
	awsRegion    string

	noClaude bool
	noGemini bool
	noOpenAI bool
	noCodex  bool // deprecated alias of noOpenAI; also skips specs named codex
	noAzure  bool

	// judges are the adjudication panel: each is a persona (built-in focus or a
	// custom prompt file) on a model. judgeModels overrides a judge's model.
	judges      []string // --judge: "name" or "name=/path/prompt.md" (repeatable)
	judgeModels []string // --judge-model: "name=<voter name>" (repeatable; codex is a deprecated alias of openai)

	// analysts is the triage-analyzer panel: personas (built-in lens or a custom
	// prompt file) that vote as extra analysts on the preferred model, so a
	// single-model run still gets an ensemble vote.
	analysts []string // --analyst: "name" or "name=/path/prompt.md" (repeatable)

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
	root := newRootCmd(cfg)
	if err := root.Execute(); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}

// newRootCmd builds the root cobra command with every flag wired onto cfg.
// Tests use it to parse flag matrices without running a triage.
func newRootCmd(cfg *config) *cobra.Command {
	root := &cobra.Command{
		Use:          "harmonia [flags] INPUT_FILE",
		Short:        "Experimental multi-model security finding triage",
		Version:      version,
		Args:         cobra.ExactArgs(1),
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			return run(cmd.Context(), cfg, args[0])
		},
	}

	f := root.Flags()
	f.StringVarP(&cfg.model, "model", "m", "claude-opus-5", "Claude model id (the claude preset)")
	f.StringVar(&cfg.geminiModel, "gemini-model", "gemini-2.5-flash", "Gemini model id (the gemini preset)")
	f.StringVar(&cfg.openaiModel, "openai-model", "gpt-5.6-sol", "OpenAI model id (the openai preset)")
	f.StringVar(&cfg.azureDeployment, "azure-deployment", "gpt-5.5", "Azure OpenAI deployment name (the azure preset)")
	f.StringVar(&cfg.azureEndpoint, "azure-endpoint", "", "Azure endpoint URL (else AZURE_OPENAI_ENDPOINT)")
	f.StringVar(&cfg.azureAPIVersion, "azure-api-version", "2024-12-01-preview", "Azure API version")
	f.StringVar(&cfg.claudeEndpoint, "claude-endpoint", "", "Anthropic-compatible endpoint URL (else the official API)")
	f.StringVar(&cfg.openaiEndpoint, "openai-endpoint", "", "OpenAI-compatible endpoint root including /v1 (else the protocol default)")

	// Model config layers: a standing harmonia.toml plus one-shot specs.
	f.StringVar(&cfg.configFile, "config", "",
		"model config file (harmonia.toml with [[models]] entries); discovered automatically when unset: harmonia.toml in the working directory, then the input file's directory")
	f.StringArrayVar(&cfg.addModels, "add-model", nil,
		"one-shot model spec 'name,key=value,...' (repeatable; per-field precedence flag > file > preset). Keys: name, protocol, endpoint, api_key, model, context_window, price_in, price_out, bedrock, region, api_version")
	f.StringArrayVar(&cfg.noModels, "no-model", nil, "skip a model by name (repeatable)")

	f.BoolVar(&cfg.useBedrock, "bedrock", false,
		"route Claude through AWS Bedrock (auto-on if CLAUDE_CODE_USE_BEDROCK or AWS_BEARER_TOKEN_BEDROCK is set); "+
			"auth is the Bedrock API key in AWS_BEARER_TOKEN_BEDROCK, else the AWS SigV4 credential chain")
	f.StringVar(&cfg.bedrockModel, "bedrock-model", "us.anthropic.claude-sonnet-4-5-20250929-v1:0",
		"Bedrock model or inference-profile ID for Claude (must be enabled in your account)")
	f.StringVar(&cfg.awsRegion, "aws-region", "", "AWS region for Bedrock (else AWS_REGION / AWS_DEFAULT_REGION)")

	f.StringVar(&cfg.apiKey, "api-key", "", "Anthropic API key (else ANTHROPIC_API_KEY)")
	f.StringVar(&cfg.googleKey, "google-api-key", "", "Google API key (else GOOGLE_API_KEY)")
	f.StringVar(&cfg.openaiKey, "openai-api-key", "", "OpenAI API key (else OPENAI_API_KEY)")
	f.StringVar(&cfg.azureKey, "azure-api-key", "", "Azure OpenAI API key (else AZURE_OPENAI_API_KEY)")

	f.BoolVar(&cfg.noClaude, "no-claude", false, "skip the Claude preset")
	f.BoolVar(&cfg.noGemini, "no-gemini", false, "skip the Gemini preset")
	f.BoolVar(&cfg.noOpenAI, "no-openai", false, "skip the OpenAI preset")
	f.BoolVar(&cfg.noAzure, "no-azure", false, "skip the Azure preset")

	// Deprecated codex aliases: still honored, hidden from --help.
	f.StringVar(&cfg.openaiModel, "codex-model", "gpt-5.6-sol", "deprecated alias of --openai-model")
	f.StringVar(&cfg.openaiKey, "codex-api-key", "", "deprecated alias of --openai-api-key")
	f.BoolVar(&cfg.noCodex, "no-codex", false, "deprecated alias of --no-openai; also skips any model named codex")
	if err := f.MarkDeprecated("codex-model", "use --openai-model"); err != nil {
		panic(err)
	}
	if err := f.MarkDeprecated("codex-api-key", "use --openai-api-key"); err != nil {
		panic(err)
	}
	if err := f.MarkDeprecated("no-codex", "use --no-openai"); err != nil {
		panic(err)
	}

	f.StringArrayVar(&cfg.judges, "judge", nil,
		"adjudication panel: a judge persona. Built-in names: adjudicator | strict | business | codeflow, or 'name=/path/prompt.md' to supply your own focus file (the JSON output contract is appended automatically). Repeatable. Every judge defaults to the preferred model (override with --judge-model), so a single-model run still yields a diverse panel")
	f.StringArrayVar(&cfg.judgeModels, "judge-model", nil,
		"override a judge's model: 'name=<voter name>' (repeatable; codex is an alias of openai); a judge without this uses the default preferred model")

	f.StringArrayVar(&cfg.analysts, "analyst", nil,
		"triage-analyzer panel: an analyst persona. Built-in names: strict | business | codeflow, or 'name=/path/prompt.md' to supply your own lens (the triage JSON output contract is preserved). Repeatable. Every analyst runs on the preferred model and votes as an extra voter, so a single-model run still yields an ensemble vote that can reach a tie")

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
	return root
}

func run(ctx context.Context, cfg *config, input string) error {
	if cfg.contextStrategy != "shared" && cfg.contextStrategy != "per-model" {
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

	// The model config is only needed where models are resolved, so a
	// parse-only run never depends on it (a stale --config path must not
	// abort a dry run).
	configFile, err := discoverConfigFile(cfg, input)
	if err != nil {
		return err
	}

	voters, err := resolveSpecs(cfg, configFile)
	if err != nil {
		return err
	}
	infos := modelInfos(voters)
	names := report.DisplayNames(infos)

	// Use the live TUI on an interactive terminal unless --plain is set; a
	// non-TTY stdout (pipe, redirect, CI) always gets plain line output.
	useTUI := !cfg.plain && term.IsTerminal(int(os.Stdout.Fd()))

	// The first voter is the preferred model: judge default, analyst base, and
	// the shared-context gatherer. Claude sits first among the presets, so this
	// is identical to the old "prefer Claude, else first voter" rule whenever
	// Claude resolves.
	gatherer := voters[0].(engine.ContextGatherer)

	judges, judgeNames := buildJudges(voters, cfg)
	analysts, analystNames := buildAnalysts(voters, cfg)

	// Analysts join the vote as extra voters on the preferred model; the engine
	// sees models + analysts, while the report keeps them separate (Meta.Models
	// vs Meta.Analysts). Build the combined set without mutating the originals
	// (buildJudges above already reads voters[0] as the preferred model).
	allVoters := voters
	if len(analysts) > 0 {
		allVoters = append(append([]provider.Provider{}, voters...), analysts...)
	}

	strategy, err := engine.NewStrategy(cfg.contextStrategy, gatherer)
	if err != nil {
		return err
	}

	if !useTUI {
		if configFile != "" {
			fmt.Printf("Config: %s\n", configFile)
		}
		banner := "Active models: " + strings.Join(names, ", ")
		if up := report.UnpricedNames(infos); len(up) > 0 {
			banner += " (unpriced: " + strings.Join(up, ", ") + ")"
		}
		fmt.Println(banner)
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
		if len(judgeNames) > 1 {
			fmt.Printf("Judge panel: %s\n", strings.Join(judgeNames, ", "))
		} else {
			fmt.Printf("Judge: %s\n", judgeNames[0])
		}
		if len(analystNames) > 0 {
			note := ""
			if strategy.Name() == "per-model" {
				note = " (per-model: one agentic loop per analyst — shared strategy is cheaper)"
			}
			fmt.Printf("Analyst panel: %s%s\n", strings.Join(analystNames, ", "), note)
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
		Voters:       allVoters,
		Strategy:     strategy,
		Judges:       judges,
		SrcRoot:      cfg.srcRoot,
		ContextRoots: contextRoots,
		Effort:       cfg.effort,
	}

	var results []report.FindingResult
	var cost float64

	if useTUI {
		hdr := tui.Header{
			InputFile: input, Models: names, Unpriced: report.UnpricedNames(infos),
			Analysts: analystNames, Strategy: strategy.Name(),
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
		Models:          infos,
		Analysts:        analystNames,
		Judges:          judgeNames,
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

// discoverConfigFile resolves the model config file: an explicit --config wins,
// then harmonia.toml in the working directory, then harmonia.toml in the input
// file's directory. It returns "" for a presets-only run and an error when
// --config points at a missing or unreadable file.
func discoverConfigFile(cfg *config, input string) (string, error) {
	if cfg.configFile != "" {
		if !isRegularFile(cfg.configFile) {
			return "", fmt.Errorf("config file not found: %s", cfg.configFile)
		}
		return cfg.configFile, nil
	}
	if isRegularFile("harmonia.toml") {
		return "harmonia.toml", nil
	}
	if dir := filepath.Dir(input); dir != "." && dir != "" {
		if p := filepath.Join(dir, "harmonia.toml"); isRegularFile(p) {
			return p, nil
		}
	}
	return "", nil
}

func isRegularFile(path string) bool {
	info, err := os.Stat(path)
	return err == nil && !info.IsDir()
}

// redactSpec hides the api_key value in a --add-model spec string before the
// spec is echoed in an error message: the flag's keys include api_key, so a
// parse error must never repeat a literal credential.
func redactSpec(spec string) string {
	parts := strings.Split(spec, ",")
	for i, kv := range parts {
		key, _, ok := strings.Cut(kv, "=")
		if ok && strings.TrimSpace(key) == "api_key" {
			parts[i] = "api_key=***"
		}
	}
	return strings.Join(parts, ",")
}

// parseAddModel parses a one-shot --add-model spec: the name before the first
// comma, then key=value pairs split at the first '=' (so values may contain
// '='), each assigned through provider.ModelSpec.Set (the single home of the
// key registry). A name alone yields a spec carrying only the name, which
// validation rejects as incomplete. Unknown keys and malformed pairs are
// errors, with the echoed spec redacted.
func parseAddModel(spec string) (provider.ModelSpec, error) {
	var m provider.ModelSpec
	echo := redactSpec(spec)
	parts := strings.Split(spec, ",")
	m.Name = strings.TrimSpace(parts[0])
	for _, kv := range parts[1:] {
		key, val, ok := strings.Cut(kv, "=")
		if !ok {
			return m, fmt.Errorf("invalid --add-model %q: %q is not key=value", echo, kv)
		}
		if err := m.Set(strings.TrimSpace(key), strings.TrimSpace(val)); err != nil {
			return m, fmt.Errorf("invalid --add-model %q: %w", echo, err)
		}
	}
	return m, nil
}

// presetSpecs builds the preset layer from the per-vendor flags and env vars,
// in resolution order (claude, gemini, openai, azure): the spec values the
// legacy hardcoded providers used, with flag values overriding the preset
// defaults. Empty keys and endpoints fall through to the protocol's env
// defaults inside the factory (resolveKey), exactly as the legacy constructors
// did.
func presetSpecs(cfg *config) []provider.ModelSpec {
	claude := provider.PresetClaude
	claude.Model = firstNonEmpty(cfg.model, claude.Model)
	claude.APIKey = cfg.apiKey
	claude.Endpoint = cfg.claudeEndpoint
	if cfg.useBedrock || envTruthy("CLAUDE_CODE_USE_BEDROCK") || os.Getenv("AWS_BEARER_TOKEN_BEDROCK") != "" {
		claude.Bedrock = true
		claude.Model = firstNonEmpty(cfg.bedrockModel, claude.Model)
		claude.Region = firstNonEmpty(cfg.awsRegion, os.Getenv("AWS_REGION"), os.Getenv("AWS_DEFAULT_REGION"))
	}

	gemini := provider.PresetGemini
	gemini.Model = firstNonEmpty(cfg.geminiModel, gemini.Model)
	gemini.APIKey = cfg.googleKey

	openai := provider.PresetOpenAI
	openai.Model = firstNonEmpty(cfg.openaiModel, openai.Model)
	openai.APIKey = cfg.openaiKey
	openai.Endpoint = cfg.openaiEndpoint

	azure := provider.PresetAzure
	azure.Model = firstNonEmpty(cfg.azureDeployment, azure.Model)
	azure.APIKey = cfg.azureKey
	azure.Endpoint = cfg.azureEndpoint
	azure.APIVersion = firstNonEmpty(cfg.azureAPIVersion, azure.APIVersion)

	return []provider.ModelSpec{claude, gemini, openai, azure}
}

// resolveSpecs builds the voter set from the config layers: preset specs from
// flags/env, the discovered harmonia.toml, and one-shot --add-model specs. The
// layers merge per field (flag > file > preset), the result validates, then
// every resolvable spec constructs through NewFromSpec in
// preset-then-declaration order, skipping the rest with a reason. voters[0] is
// the preferred model; when no spec resolves it errors listing every skip
// reason.
func resolveSpecs(cfg *config, configFile string) ([]provider.Provider, error) {
	var fileSpecs []provider.ModelSpec
	if configFile != "" {
		specs, err := provider.LoadTOML(configFile)
		if err != nil {
			return nil, err
		}
		fileSpecs = specs
	}
	var flagSpecs []provider.ModelSpec
	for _, s := range cfg.addModels {
		spec, err := parseAddModel(s)
		if err != nil {
			return nil, err
		}
		flagSpecs = append(flagSpecs, spec)
	}

	presets := presetSpecs(cfg)
	merged := provider.MergeSpecs(presets, fileSpecs, flagSpecs)
	presetNames := make(map[string]bool, len(presets))
	for _, p := range presets {
		presetNames[p.Name] = true
	}
	if err := provider.ValidateSpecs(merged, presetNames); err != nil {
		return nil, err
	}

	skipFlag := map[string]string{}
	if cfg.noClaude {
		skipFlag["claude"] = "--no-claude"
	}
	if cfg.noGemini {
		skipFlag["gemini"] = "--no-gemini"
	}
	if cfg.noOpenAI {
		skipFlag["openai"] = "--no-openai"
	}
	if cfg.noAzure {
		skipFlag["azure"] = "--no-azure"
	}
	if cfg.noCodex { // deprecated alias: skips the openai preset and any spec named codex
		skipFlag["openai"] = "--no-codex"
		skipFlag["codex"] = "--no-codex"
	}
	for _, n := range cfg.noModels {
		skipFlag[strings.TrimSpace(n)] = "--no-model"
	}

	var voters []provider.Provider
	var skipReasons []string
	for _, spec := range merged {
		if flag, skipped := skipFlag[spec.Name]; skipped {
			skipReasons = append(skipReasons, fmt.Sprintf("%s: skipped by %s", spec.Name, flag))
			continue
		}
		if ok, reason := spec.Resolvable(); !ok {
			line := fmt.Sprintf("%s: %s", spec.Name, reason)
			fmt.Printf("  skipping %s\n", line)
			skipReasons = append(skipReasons, line)
			continue
		}
		p, err := provider.NewFromSpec(spec, cfg.maxTokens)
		if err != nil {
			line := fmt.Sprintf("%s: %s", spec.Name, short(err.Error()))
			fmt.Printf("  skipping %s\n", line)
			skipReasons = append(skipReasons, line)
			continue
		}
		voters = append(voters, p)
	}
	if len(voters) == 0 {
		return nil, fmt.Errorf("no models available; %s", strings.Join(skipReasons, "; "))
	}
	return voters, nil
}

// modelInfos snapshots each voter's identity for the structured report
// metadata and the unpriced markers: name, model id, context window, and
// priced flag, in voter order.
func modelInfos(voters []provider.Provider) []report.ModelInfo {
	infos := make([]report.ModelInfo, 0, len(voters))
	for _, v := range voters {
		info := report.ModelInfo{Name: v.Name(), Model: v.Model()}
		if lp, ok := v.(*provider.LLMProvider); ok {
			info.ContextWindow = lp.ContextWindow()
			info.Priced = lp.Priced()
		}
		infos = append(infos, info)
	}
	return infos
}

// buildJudges assembles the adjudication panel from the --judge / --judge-model
// flags. Each judge is a persona (a built-in focus or a custom prompt file) on a
// model. Every judge defaults to the preferred model (voters[0] — Claude if
// present, else the first available), so a single-model run still gets a diverse
// panel; --judge-model overrides a judge's model. With no --judge flags the panel
// is a single default judge named "adjudicator" (the legacy behavior). It returns
// the panel and the judge names (for the banner and report metadata).
func buildJudges(voters []provider.Provider, cfg *config) ([]engine.Judge, []string) {
	if len(voters) == 0 {
		return nil, nil
	}
	defaultModel := voters[0].(*provider.LLMProvider)

	modelByName := func(name string) (*provider.LLMProvider, bool) {
		if name == "codex" { // deprecated alias of the openai voter
			name = "openai"
		}
		for _, v := range voters {
			if v.Name() == name {
				return v.(*provider.LLMProvider), true
			}
		}
		return nil, false
	}

	models := map[string]string{}
	for _, spec := range cfg.judgeModels {
		if name, model := splitSpec(spec); name != "" {
			models[name] = model
		}
	}

	var judges []engine.Judge
	var names []string
	for _, spec := range cfg.judges {
		name, file := splitSpec(spec)
		if name == "" {
			name = "judge"
		}
		var system string
		if file != "" {
			data, err := os.ReadFile(file)
			if err != nil {
				fmt.Printf("  skipping judge %q: %s\n", name, short(err.Error()))
				continue
			}
			system = triage.BuildJudgeSystemPrompt(string(data))
		} else {
			focus, ok := triage.FocusFor(name)
			if !ok {
				fmt.Printf("  skipping judge %q: unknown persona (built-in: %s) or use name=/path/prompt.md\n",
					name, strings.Join(triage.BuiltInPersonaNames(), ", "))
				continue
			}
			system = triage.BuildJudgeSystemPrompt(focus)
		}
		model := defaultModel
		if m, ok := models[name]; ok {
			if pm, ok := modelByName(m); ok {
				model = pm
			} else {
				fmt.Printf("  warning: judge %q model %q unavailable; using default\n", name, m)
			}
		}
		judges = append(judges, provider.NewJudge(name, model, system))
		names = append(names, name)
	}

	if len(judges) == 0 {
		judges = []engine.Judge{provider.NewJudge("adjudicator", defaultModel, triage.AdjudicationSystemPrompt)}
		names = []string{"adjudicator"}
	}
	return judges, names
}

// buildAnalysts assembles the triage-analyzer panel from the --analyst flags.
// Each analyst is a persona (a built-in lens or a custom prompt file) running on
// the preferred model (voters[0]) — deliberately no model override, since the
// point is ensemble diversity on a single model. Analysts join the vote as
// ordinary voters. With no --analyst flags the panel is empty (default unchanged).
func buildAnalysts(voters []provider.Provider, cfg *config) ([]provider.Provider, []string) {
	if len(voters) == 0 {
		return nil, nil
	}
	base := voters[0].(*provider.LLMProvider)

	// Built-in analysts are the "lens" personas (the neutral "adjudicator" judge
	// persona is not a valid analyst lens). A custom name may be supplied via a
	// prompt file instead.
	valid := make(map[string]bool, len(triage.AnalystPersonaNames()))
	for _, n := range triage.AnalystPersonaNames() {
		valid[n] = true
	}

	var analysts []provider.Provider
	var names []string
	for _, spec := range cfg.analysts {
		name, file := splitSpec(spec)
		if name == "" {
			continue
		}
		var focus string
		if file != "" {
			data, err := os.ReadFile(file)
			if err != nil {
				fmt.Printf("  skipping analyst %q: %s\n", name, short(err.Error()))
				continue
			}
			focus = string(data)
		} else if !valid[name] {
			fmt.Printf("  skipping analyst %q: unknown persona (built-in: %s) or use name=/path/prompt.md\n",
				name, strings.Join(triage.AnalystPersonaNames(), ", "))
			continue
		} else {
			focus, _ = triage.FocusFor(name)
		}
		analysts = append(analysts, provider.NewAnalyst(base, name, focus))
		names = append(names, name)
	}
	return analysts, names
}

// splitSpec splits a "name=value" spec on the first '='. No '=' (or an empty
// value) yields an empty value — e.g. a bare built-in persona name.
func splitSpec(spec string) (string, string) {
	name, value, _ := strings.Cut(spec, "=")
	return strings.TrimSpace(name), strings.TrimSpace(value)
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
