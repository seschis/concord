# CLAUDE.md — concord

Guidance for Claude Code working in this repo. Read `DESIGN.md` for the full
architecture; this file captures the non-obvious things that will trip you up.

## What this is

An experimental Go tool that triages security scanner findings with up to four
LLMs (Claude, Gemini, Codex/direct-OpenAI, Azure OpenAI), takes a majority
vote, and adjudicates ties.

Hard constraints, do not violate these.

- Local files only. No backend calls, no LLM proxy, no usage reporting.
- No telemetry. No OpenTelemetry, no OpenLIT.
- Output is `results.json` (with a `concordAnalysis` block) plus
  `report.md`. Do not write an enhanced SARIF.

## Build and test

```
make build     # -> ./bin/concord, version-stamped from git describe
make test      # go test ./...
make check     # fmt + vet + test
make snapshot  # local goreleaser build, no publish
```

Go 1.26+. LLM dependency is `github.com/tmc/langchaingo` (pinned v0.1.14).

## Package layout and the import rule

Dependencies flow one way. Keep it that way or you will create an import cycle.

```
finding    (no internal deps)
progress   (no internal deps)              (realtime Event/Sink; threaded via context)
transcript (no internal deps)              (agentic-loop Step/Writer; threaded via context, like progress)
cvss       (no internal deps)              (CVSS 4.0 vector parsing, scoring, severity rating)
bundle     (no internal deps)              (context-dir export/import: manifest, scan, tar.gz bundle)
triage     -> finding                      (Result, Verdict, prompts, parsing, adjudication types)
agent      -> langchaingo/llms only        (tool loop + sandboxed file tools; NO finding/triage/provider)
provider   -> agent, finding, progress, transcript, triage   (LLMProvider base + 4 constructors + pricing)
ingest     -> finding
report     -> cvss, finding, triage
engine     -> provider, report, progress, triage, finding   (strategy, vote, adjudicate orchestrator)
tui        -> progress + bubbletea/bubbles/lipgloss   (live dashboard; consumes Events)
cmd        -> everything
```

Nothing imports `engine`. Strategy and orchestration live there (not in `triage`)
specifically to avoid `triage <-> provider` cycles.

## Realtime progress (TUI)

`progress.Sink` is threaded through `context.Context` (`progress.WithSink` /
`progress.From`), NOT added to interfaces, so the one-way graph above is
unchanged. Emit points: engine (RunStart/FindingStart/FindingDone/RunDone) and
provider (Action per model call + ModelDone per verdict, tagged with a Role:
voter/explorer/adjudicator). The agentic tool loop reports each call via
`agent.LoopOptions.OnStep` (a `agent.Step`, kept in-package so `agent` stays
langchaingo-only); `provider.stepReporter` turns steps into `Action` events and
prices them per call so cost accrues live.

Cost accounting to avoid double counting a live total: single-shot calls carry
cost on `ModelDone`; agentic calls carry it on the per-step `Action` events and
send `ModelDone` with `CostUSD: 0`. The report's authoritative total still comes
from `report`/`r.CostUSD`, not from summed events.

`cmd` uses the Bubble Tea TUI (`internal/tui`) when stdout is a TTY and `--plain`
is not set; otherwise it prints plain per-finding lines. The TUI's `Model.Update`
is a pure reducer (`apply`), unit-tested without a terminal. On Ctrl+C it cancels
the run context and waits for the engine goroutine's doneMsg before quitting, so
results are never read while still being written.

## Per-finding transcripts

Every agentic call (voter or explorer) writes its tool-call history to
`<output-dir>/transcripts/<findingID>_<role>_<provider>.jsonl` (one JSON line per
model turn: iteration, every tool call with its args and result, any interstitial
text, token counts, and whether it was the final answer). This is separate from
the live `progress.Sink` and follows the same pattern: a `transcript.Writer`
threaded through `context.Context` (`transcript.WithWriter` / `transcript.From`,
defaulting to a `Nop`), not added to interfaces, so the one-way graph is
unchanged. `agent.RunToolLoop`'s `OnStep` now fires once per iteration *after*
executing that iteration's tool calls (previously it fired before, since it only
reported the display summary); `provider.stepReporter` fans each `agent.Step` out
to both the progress sink and the transcript writer. On by default; disable with
`--no-transcripts`. A transcript failure (e.g. an unwritable output dir) is
swallowed — it must never break a triage run.

## Gotchas that are easy to get wrong

- **Never read `resp.Choices[0]` directly.** Anthropic returns one choice per
  content block, an empty "thinking" block, each `tool_use` as its own choice,
  then the text. Always use `agent.Collapse(resp)`, which combines text,
  aggregates tool calls across all choices, and reads token usage once (usage is
  duplicated across choices, not additive, so do not sum it).

- **Both Claude paths run on `anthropic-sdk-go`, not langchaingo.** `NewClaude`
  (direct API) and `NewClaudeBedrock` (Bedrock) both build one adapter,
  `provider.anthropicNativeModel` in `anthropic_native.go`, that implements
  langchaingo's `llms.Model` so the tool loop / single-shot / adjudicate paths are
  unchanged. langchaingo only backs Gemini, OpenAI (Codex), and Azure now. The
  adapter is why prompt caching works; keep any new Claude-request tuning in the
  adapter's `buildParams` / `applyReasoning`, not in langchaingo call options.

- **Prompt caching is on by default (both direct and Bedrock).** The adapter
  places two `cache_control` breakpoints per request: the last system block
  (caches system+tools together) and the last content block of the last message
  (caches the running conversation prefix), TTL 5m. Explicit `cache_control` is GA
  on all current models on both paths; below a model's minimum cacheable prefix
  (1024–4096 tok) it silently no-ops, so there is no model gate. Cache tokens flow
  through `agent.Usage` (`CacheWrite`/`CacheRead`), the per-finding transcript
  Step, and the cache-aware cost functions (cache read 0.1x input; cache write
  1.25x input for the 5m TTL — it would be 2x for a 1h TTL, so `cacheWriteMult` in
  `pricing.go` is kept in sync with `cacheTTL` in `constructors.go`). If
  `cache_read_input_tokens` stays 0 across a multi-turn run, a silent invalidator
  is at work — the prefix must be byte-identical turn to turn.

- **Effort/thinking translation is model-capability-dependent.** langchaingo's
  `WithThinkingMode` maps to the *legacy* `budget_tokens` thinking API, which 400s
  on current models (Opus 4.7/4.8, Sonnet 5, Fable 5). The adapter's
  `applyReasoning` classifies the model via `thinkingSupportFor` (`pricing.go`)
  into three states, not two:
  - **adaptive** (Opus 4.6/4.7/4.8, Sonnet 5/4.6, Fable/Mythos 5, `*-5`):
    `thinking:{type:"adaptive"}` + `output_config.effort`, and **no** temperature
    (these models 400 on it).
  - **budget** (Opus 4.0/4.1/4.5, Sonnet 4.0/4.5, Haiku 4.5, Claude 3.7 Sonnet):
    `budget_tokens` + temperature, preserving the old behavior.
  - **none** — models with no extended thinking (Claude 3.5 Sonnet/Haiku, Claude 3
    Opus/Haiku) **and any unknown/unlisted ID**: omit BOTH thinking and
    temperature. That is the only 400-safe default — a no-thinking model rejects
    `budget_tokens`, and an unknown ID could be a newer current model that rejects
    both. A new current model therefore runs without thinking until it is added to
    `adaptiveThinkingModels`; that is the deliberate safe tradeoff.
  `thinkingOpts` still emits `WithThinkingMode` + temperature 1; the adapter
  reinterprets/drops those. On agentic calls no thinking config is sent, so
  thinking stays OFF (as before); the loop's `temperature=0` is dropped for
  adaptive/none models.

- **`--effort` on the agentic path** still sets only the tool-iteration budget
  (`agent.MaxItersForEffort`); it does not enable thinking there.

- **Claude via Bedrock** (`--bedrock`, auto-on when `CLAUDE_CODE_USE_BEDROCK` or
  `AWS_BEARER_TOKEN_BEDROCK` is set) uses `anthropic-sdk-go/bedrock`
  (`bedrock.WithLoadDefaultConfig`). Auth flows through the AWS credential chain
  the helper loads: a Bedrock API key in `AWS_BEARER_TOKEN_BEDROCK` takes
  precedence, else the SigV4 chain (`AWS_PROFILE` / SSO / env / IAM role). A region
  is required either way (endpoint resolution). Credentials resolve lazily, so a
  bad token/profile surfaces as a per-finding error result, not at startup. Unlike
  the old langchaingo Bedrock path, thinking and caching are now wired the same as
  the direct API. Model ID is a Bedrock/inference-profile ID (`--bedrock-model`,
  e.g. `us.anthropic.claude-sonnet-4-5-...-v1:0`), priced approximately via
  `costClaudeBedrock` (substring match, not prefix).

- **Extra architecture context** (`--context-dir`, repeatable, requires
  `--srcroot`). The `agent.ToolBox` is now multi-root: `NewToolBox(primary,
  context...)` where the primary (the finding's repo) is reserved label `src` and
  each context dir is a sandboxed root labeled by its basename. When >1 root
  exists the file tools gain an optional `root` argument (enum of labels); with a
  single root the schema is byte-identical to before, so nothing changed for runs
  without context. `ToolBox.Manifest()` returns a short inventory of the context
  roots that `provider.manifestBlock` injects into the explorer/agentic prompt so
  the model knows what exists and reads on demand (cheap) rather than everything
  being stuffed in. Each root keeps its own `safeJoin` sandbox. Threaded as
  `agent.Root` through `engine.Engine.ContextRoots` -> `Opts` -> `provider.ToolEnv`
  and the `ContextGatherer.Gather` signature. v1 is text/code/config/text-diagrams
  only; image diagrams (PNG/SVG) are deferred (would need a multimodal path, the
  agent loop is text-only).

- **Architecture guide** (`--context-guide FILE`, or a `TRIAGE_CONTEXT.md` at a
  context-root top level, auto-inlined). A large context root is reachable but not
  discoverable, so a short human-written map (where the gateway/services/policies
  live, using paths relative to a context root) is injected into the brief ahead
  of the auto-listing. The tools can then follow those relative paths since they
  stay inside the sandbox (absolute paths and `..` are refused; note a symlink
  inside a root IS followed today, a known lexical-sandbox gap). Explicit guide
  text is threaded via context (`triage.WithGuide`/`GuideFrom`), like the progress
  sink; convention files are read by `ToolBox.Manifest` from each context root.
  Both require `--srcroot`. Each guide source is capped at `maxGuideChars`.

- **Agent file tools are sandboxed** to `--srcroot` via `agent.(*ToolBox).safeJoin`
  (cleaned-prefix check). Keep any new tool inside that guard.

## Common changes

- **Add a provider**, write a constructor in `provider/constructors.go` returning
  `*LLMProvider` with the right client (langchaingo for non-Claude, the
  `anthropicNativeModel` adapter for Claude) and a cost function, add a pricing
  table + `cost*` func in `provider/pricing.go`, and register it in
  `cmd/concord/main.go` `resolveProviders`. The base handles single-shot,
  agentic, gather, and adjudicate for free. `cost*` funcs take
  `(model, in, out, cacheWrite, cacheRead int)`; non-Claude providers report zero
  cache tokens, so those terms vanish.

- **Add an input format**, add a `parse*` in `internal/ingest` and a case in
  `ingest.Load`. Map to the normalized `finding.Finding`.

- **Verdict to classification** lives in `triage.Classification`
  (REAL -> TRUE_POSITIVE, UNLIKELY/NOT_EXPLOITABLE -> FALSE_POSITIVE,
  NEEDS_MORE_CONTEXT -> UNKNOWN).

## Conventions

- Run `make check` before committing. Tests are colocated (`*_test.go`) and must
  pass offline (no network, no API keys). Live model paths are only reachable
  with credentials, so cover logic (vote, parse, ingest, tools, collapse) with
  unit tests, not live calls.
- goreleaser publishes plain GitHub release binaries (darwin/linux, amd64/arm64)
  via `release --clean`. No Homebrew tap, no custom download strategy.
- End commit messages with the Co-Authored-By trailer used across this repo.

## Status

Design phases 1 through 5 plus the effort wiring are done and pushed to
`main`. Live multi-model runs have not been exercised (no API keys in the build
sessions), so the first real run should confirm the reasoning knobs and the
agentic loop against actual endpoints.
