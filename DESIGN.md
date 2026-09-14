# concord — Design

Status: implemented. Phases 1 through 5 of the phased plan (section 13) are
complete; the remaining items are in section 14.

Module path, `github.com/seschis/concord`.

## 1. What this is

`concord` is a Go tool for triaging security scanner findings. It reads
scanner findings, optionally reads the source code they point at, asks four
LLMs to judge each finding, takes a majority vote, and adjudicates ties with a
single model. It reads and writes only local files.

It is an experimental tool with no backend integration, no server-side cache,
and no usage reporting to any external service.

## 2. Goals and non-goals

### Goals

- An on-demand, tool-calling code-reading agent that can read the finding's
  own repo (and optionally sibling repos) to judge exploitability.
- A quad-model vote and adjudication (Claude, Gemini, Codex, Azure).
- Accept SARIF, JSON, CSV, Markdown, and XLSX as input.
- Produce a Markdown report and a JSON results file, the latter carrying a
  `concordAnalysis` block per finding.
- Offer two context strategies behind one interface, selectable on the command
  line, with shared-context as the default.
- Ship via goreleaser to GitHub releases.

### Non-goals

- No backend connections or concepts. No LLM proxy, no usage reporting, no
  server-side triage cache, no fingerprint lookups.
- No OpenTelemetry, no OpenLIT, no external telemetry of any kind.
- No enhanced-SARIF output. The tool does not write `concordAnalysis` back
  into SARIF result properties. That data rides inside the JSON report
  instead.

## 3. Dependencies

- `github.com/tmc/langchaingo` for the Gemini, Codex (OpenAI), and Azure
  providers and their tool calling, via `llms/openai` (also covers Azure through
  `WithBaseURL` + `WithAPIVersion`) and `llms/googleai` / `llms/vertex`, plus
  `llms.Tool` / `llms.WithTools` and `ToolCall` / `ToolCallResponse` handling.
- `github.com/anthropics/anthropic-sdk-go` for both Claude paths, the direct API
  and Bedrock (`anthropic-sdk-go/bedrock`). A single adapter,
  `provider.anthropicNativeModel`, implements langchaingo's `llms.Model` so the
  tool loop, single-shot, and adjudicate paths are provider-agnostic. This
  adapter is what makes prompt caching and per-model thinking translation
  possible; langchaingo's `llms/anthropic` and `llms/bedrock` are no longer used.
- `github.com/spf13/cobra` for the CLI.
- `github.com/xuri/excelize/v2` for XLSX ingest.
- `golang.org/x/sync/errgroup` for bounded fan-out.
- Standard library for JSON, CSV, SARIF (unmarshalled into structs), and file
  tools.

### Known langchaingo caveats to resolve during build

langchaingo has no `create_agent`, no `deepagents` middleware, and no
`structured_response`. The agent loop, the middleware-equivalent concerns (token
tracking, optional prompt caching), and JSON-structured output parsing are
hand-rolled. This is more code but removes a large dependency surface.

Provider-specific tuning knobs. Reasoning is wired, `--effort` maps to a Claude
extended-thinking config and an OpenAI/Azure `reasoning_effort` on the
single-shot paths (voters and adjudication); the agentic paths use effort for the
tool-iteration budget instead, since multi-turn loops cannot round-trip Claude
thinking blocks. For Claude the adapter classifies each model and emits the right
API, `thinking:{type:"adaptive"}` + `output_config.effort` on current models,
legacy `budget_tokens` on older ones, and nothing on models without extended
thinking (also the safe default for unknown IDs). Anthropic prompt caching
(`cache_control`) is now wired and on by default on both Claude paths (two
breakpoints, 5m TTL; cost-aware pricing in `pricing.go`). One knob remains,
Gemini `thinking_budget` is accepted but ignored by langchaingo v0.1.14; it does
not block the project.

## 4. Repository layout

```
concord/
  cmd/concord/main.go             # cobra CLI entrypoint
  internal/
    finding/finding.go            # normalized Finding shape
    ingest/
      detect.go                   # dispatch on extension + content
      sarif.go json.go csv.go markdown.go xlsx.go
    provider/
      provider.go                 # Provider interface, AnalyzeInput, registry
      base.go                     # LLMProvider base: single-shot/agentic/gather/adjudicate
      constructors.go             # the 4 provider constructors (Claude/Gemini/Codex/Azure)
      anthropic_native.go         # anthropic-sdk-go adapter: caching + thinking translation
      pricing.go                  # per-1M token cost tables (cache-aware) + cost()
    agent/
      loop.go                     # hand-rolled tool-calling ReAct loop
      tools.go                    # read_file, list_dir, search, sandboxed to srcroot
      gather.go                   # ContextGatherer, the shared explorer
      prompts.go                  # 3-part triage system prompt + JSON schema
    triage/
      strategy.go                 # Strategy interface + factory
      shared.go per_model.go      # the two strategies
      orchestrator.go             # per-finding driver, strategy-agnostic
      vote.go adjudicate.go       # majority vote + Claude adjudication
      analysis.go                 # Result, verdict categories, mapping to classification
    report/
      markdown.go json.go         # outputs + concordAnalysis block
  .goreleaser.yaml
  .github/workflows/release.yml
  go.mod
  README.md
  DESIGN.md
```

## 5. Core model

### Finding

A normalized finding. Fields, `ID`, `File`, `Line`, `VulnType`, `Severity`,
`CWE`, `Description`, `Code`, `Fix`, `SourceTool`, and a `Raw` map for
format-specific extras.

### Provider

Each model is a `Provider`. It exposes both call modes through `AnalyzeInput`.
Exactly one of `SharedContext` or `Tools` is set, and the strategy guarantees
which.

```go
type Provider interface {
    Name() string          // "claude" | "gemini" | "codex" | "azure"
    Model() string
    Analyze(ctx context.Context, f finding.Finding, in AnalyzeInput) (triage.Result, error)
}

type AnalyzeInput struct {
    SharedContext string    // non-empty -> single-shot with this brief injected
    Tools         *ToolEnv  // non-nil   -> run the agentic tool loop over the repo
    Effort        string    // low | medium | high
}
```

### Provider mapping

| Role | Go client | Tool calling | Notes |
|---|---|---|---|
| Claude | `anthropic-sdk-go` (direct + Bedrock) | yes | prompt caching + per-model thinking done in the adapter |
| Gemini | langchaingo `llms/googleai` or `llms/vertex` | yes | `thinking_budget` passed but ignored by v0.1.14 |
| Codex | langchaingo `llms/openai` | yes | direct GPT models via `reasoning_effort` |
| Azure | langchaingo `llms/openai` + base URL / api version | yes | `reasoning_effort` exposure |

## 6. Context strategies (the key design decision)

Context gathering is behind a `Strategy` interface. The strategy owns exactly
one decision, how the four voters get their code context. Voting, adjudication,
and reporting are common to both strategies.

```go
type Strategy interface {
    Name() string
    Run(ctx context.Context, f finding.Finding, voters []provider.Provider, opts Opts) (Run, error)
}

type Run struct {
    Results   []Result
    ExtraCost float64   // cost of the shared explorer, 0 for per-model
}

type Opts struct {
    SrcRoot string
    Effort  string
}
```

### Shared context (default)

One explorer reads the repo once, writes a brief, and all four voters analyze
single-shot against the same brief. Cheaper, and the vote stays fair because
every model sees identical evidence.

```go
type SharedContext struct {
    gatherer ContextGatherer   // Claude-backed explorer that returns text
    tools    ToolEnv
}

func (s SharedContext) Run(ctx context.Context, f finding.Finding, voters []provider.Provider, o Opts) (Run, error) {
    brief, cost, err := s.gatherer.Gather(ctx, f, s.tools, o.Effort)
    if err != nil {
        brief = ""   // degrade to metadata-only rather than failing the finding
    }
    results := fanOut(voters, func(p provider.Provider) (Result, error) {
        return p.Analyze(ctx, f, provider.AnalyzeInput{SharedContext: brief, Effort: o.Effort})
    })
    return Run{Results: results, ExtraCost: cost}, nil
}
```

### Per-model agent (opt-in)

Each voter runs its own tool loop and crawls the repo independently. Maximum
independence of the vote, roughly four times the context cost and latency.

```go
type PerModelAgent struct{ tools ToolEnv }

func (s PerModelAgent) Run(ctx context.Context, f finding.Finding, voters []provider.Provider, o Opts) (Run, error) {
    tools := s.tools
    results := fanOut(voters, func(p provider.Provider) (Result, error) {
        return p.Analyze(ctx, f, provider.AnalyzeInput{Tools: &tools, Effort: o.Effort})
    })
    return Run{Results: results}, nil
}
```

### Factory and CLI

```go
func NewStrategy(name string, tools ToolEnv, explorer provider.Provider) (Strategy, error) {
    switch name {
    case "", "shared":
        return SharedContext{gatherer: explorerGatherer{explorer}, tools: tools}, nil
    case "per-model", "quad":
        return PerModelAgent{tools: tools}, nil
    default:
        return nil, fmt.Errorf("unknown context strategy %q (want shared|per-model)", name)
    }
}
```

```go
flags.StringVar(&cfg.ContextStrategy, "context-strategy", "shared",
    "how voters get code context: shared (one explorer, cheaper) | per-model (each model crawls, richer)")
```

### Two behaviors decided here

- The shared strategy needs an explorer model. It uses Claude. If Claude is not
  among the authenticated models, the fallback is a deterministic
  service-scoping gatherer, so shared mode still works without a tool-calling
  model.
- When `--srcroot` is absent, both strategies degrade to metadata-only
  single-shot, which keeps the tool usable on findings with no local checkout.

A future third mode (for example `deterministic`, no model exploration at all)
is just another `Strategy` implementation and one more case in the factory.
Nothing in the orchestrator, voting, or reporting changes.

## 7. The agent tool loop

Hand-rolled over langchaingo. It runs until the model stops requesting tools or
a max-iteration cap (set by effort) is reached.

```go
for i := 0; i < maxIters; i++ {
    resp, err := llm.GenerateContent(ctx, msgs, llms.WithTools(fileTools))
    if err != nil { return Result{}, err }
    if resp.Choices[0].StopReason != "tool_calls" {
        return parseTriageJSON(resp.Choices[0].Content)   // final structured verdict
    }
    msgs = append(msgs, assistantWith(resp.Choices[0].ToolCalls))
    for _, tc := range resp.Choices[0].ToolCalls {
        out := runTool(srcRoot, tc)                        // sandboxed
        msgs = append(msgs, toolResult(tc.ID, out))
    }
}
```

### Tools

Three read-only tools, rooted at `--srcroot`. `read_file` (optional line range),
`list_dir`, and `search` (ripgrep if present, else a Go walk). All paths are
sandboxed with a cleaned prefix check.

```go
func safeJoin(root, rel string) (string, bool) {
    p := filepath.Clean(filepath.Join(root, rel))
    return p, strings.HasPrefix(p, filepath.Clean(root)+string(os.PathSeparator))
}
```

The shared-context explorer reuses this loop but returns a plain-text brief
rather than a verdict.

## 8. Voting and adjudication

Verdict categories, `CONFIRMED_REAL`, `LIKELY_REAL`, `UNLIKELY`,
`NOT_EXPLOITABLE`, `NEEDS_MORE_CONTEXT`. Findings group into real, not-real, and
unknown categories for agreement detection.

- If two or more voters agree on category, the most conservative agreed verdict
  stands.
- If the voters tie, a **judge panel** convenes. Each judge is a persona (a
  focus/personality system prompt) on a model; the persona is the primary axis of
  diversity, so a panel of several judges on the SAME model still disagrees in
  useful ways. All judges default to the preferred model and can be overridden
  per judge (`--judge-model`). The panel runs concurrently and its verdicts are
  combined with the same category-majority + conservative-tiebreak logic as the
  voters (`VoteJudges`), producing the final verdict and the deciding factor.
  A single default judge (`adjudicator`, the legacy prompt) preserves the old
  behavior when no `--judge` flags are given.
- Per-model and per-judge failures are tolerated. A failed voter or judge drops
  out of its vote (normalized to `NEEDS_MORE_CONTEXT`) rather than aborting the
  finding.

```go
run, _ := strategy.Run(ctx, f, voters, opts)
verdict, agreement := Vote(run.Results)
if agreement == None {
    adjudications := runJudges(ctx, judges, f, run.Results) // concurrent, stamped
    verdict, _ = VoteJudges(adjudications)
}
totalCost := sum(run.Results) + run.ExtraCost + sum(adjudications)
```

Judges are defined with `--judge` (repeatable): a built-in persona name
(`adjudicator`, `strict`, `business`, `codeflow`) or `name=/path/prompt.md` for a
custom focus file. Each persona's focus is layered over a fixed JSON output
contract so all judges on a panel emit the same comparable schema.

The same persona idea applies at the **voting** stage as the **analyst panel**:
`--analyst` (repeatable) adds analyzer "lenses" (`strict`, `business`, `codeflow`,
or `name=/path/prompt.md`) that vote as extra voters on the preferred model. This
is the single-provider case — with only one LLM available the cross-model vote
degenerates (one voter, never a tie, so the judge panel never fires), and
same-model analysts restore a real ensemble. Each analyst is a clone of the
preferred provider carrying a persona lens layered over the base triage prompt;
the existing `Vote` handles them unchanged, and in the default `shared` strategy
they run single-shot over the one shared brief, so the cost is a few extra
analysis calls rather than extra repo crawls.

## 9. Inputs

Five input formats. `detect.go` dispatches on extension and content.

- SARIF, unmarshal into structs (or `github.com/owenrumney/go-sarif`).
- JSON, our scanner format or a `{findings:[]}` or flat array.
- CSV, `encoding/csv`, column-name auto-detection.
- Markdown, regex parsing against `### FINDING N ###` and table formats.
- XLSX, `github.com/xuri/excelize/v2`.

## 10. Outputs

Two artifacts, a Markdown report with the full per-model analysis and cost
rollup, and a JSON results file. Each finding in the JSON also carries a
`concordAnalysis` object, derived from the vote.

```jsonc
{
  "id": "F1", "file": "...", "final_verdict": "LIKELY_REAL",
  "claude": {}, "gemini": {}, "codex": {}, "azure": {},
  "agreement": "majority",
  "concordAnalysis": {
    "classification": "TRUE_POSITIVE",
    "justification": "<final reasoning>",
    "workDetail": "<condensed agent trace / deciding factor>",
    "schemaVersion": "1.0.0"
  }
}
```

### Verdict to classification mapping

- `CONFIRMED_REAL`, `LIKELY_REAL` map to `TRUE_POSITIVE`.
- `UNLIKELY`, `NOT_EXPLOITABLE` map to `FALSE_POSITIVE`.
- `NEEDS_MORE_CONTEXT` maps to `UNKNOWN`.

## 11. Credentials

Resolved per provider from environment variables (and CLI flags).

- Anthropic (Claude), `ANTHROPIC_API_KEY`. Or via Bedrock with `--bedrock`
  (auto-on when `CLAUDE_CODE_USE_BEDROCK` or `AWS_BEARER_TOKEN_BEDROCK` is set):
  the AWS credential chain, a Bedrock API key in `AWS_BEARER_TOKEN_BEDROCK` first,
  else the SigV4 chain (`AWS_PROFILE` / SSO / env / IAM role); a region is
  required, and the model is a Bedrock/inference-profile ID via `--bedrock-model`.
- Google (Gemini), `GOOGLE_API_KEY` or Vertex ADC via
  `GOOGLE_APPLICATION_CREDENTIALS`.
- Codex, `OPENAI_API_KEY` for the direct backend, or AWS credentials for
  Bedrock.
- Azure OpenAI, key plus endpoint plus deployment.

A model whose credential is missing is skipped with a message, and the run
proceeds with the remaining models. At least one model is required.

## 12. Distribution

goreleaser to GitHub releases.

- `.goreleaser.yaml` builds darwin and linux for amd64 and arm64, and publishes
  the archives as GitHub release assets.
- `.github/workflows/release.yml` is a single `workflow_dispatch` job that
  validates the version, creates and pushes the tag, and runs
  `goreleaser release --clean`.
- Users download the binary directly from the releases page, or build from
  source with `go build ./cmd/concord`.

## 13. Phased plan

1. Skeleton, cobra CLI, `Finding`, ingest for SARIF and JSON, one provider
   (Claude) single-shot, JSON output. Prove the spine end to end.
2. Agent tool loop and `--srcroot` file reading, plus the `ContextGatherer`
   explorer.
3. The other three providers, the `Strategy` interface with both strategies,
   the vote, and adjudication.
4. Remaining ingest formats (CSV, Markdown, XLSX), the Markdown report, and the
   `concordAnalysis` block.
5. goreleaser and CI. Then confirm or defer the advanced provider knobs from
   section 3.

## 14. Open questions and known gaps

- Claude prompt caching and per-model thinking are resolved by moving Claude
  onto `anthropic-sdk-go` (the `anthropicNativeModel` adapter). Known gap:
  langchaingo v0.1.14 (used for Gemini/Codex/Azure) ignores Gemini's
  `thinking_budget`.
- Whether the shared-strategy fallback when Claude is absent should be the
  deterministic gatherer (recommended) or a plain no-context run.
- Whether to add a credential-manager integration path, or stay
  environment-only.
