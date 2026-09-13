# concord

Experimental security-finding triage tool. It reads and writes local files
only, no backend integration and no telemetry.

See [DESIGN.md](DESIGN.md) for the architecture and the context-strategy design.

## What it does

For each scanner finding it gathers code context (optional), asks up to four
models to judge the finding, takes a majority vote, and adjudicates ties.

- Four-model triage, Claude, Gemini, Codex (direct OpenAI), and Azure OpenAI,
  with a majority vote and Claude-preferred adjudication on ties.
- On-demand, tool-calling source reading, sandboxed to `--srcroot`.
- Two context strategies via `--context-strategy`, `shared` (one explorer,
  default) and `per-model` (each model crawls).
- Inputs, SARIF, JSON, CSV, Markdown, XLSX.
- Outputs, `results.json` (with a `concordAnalysis` block per finding) and
  a human-readable `report.md`.

## Install

Download a prebuilt binary from the [releases page](https://github.com/seschis/concord/releases).

Or build from source (Go 1.26+):

```bash
go build -o concord ./cmd/concord
```

## Usage

```bash
# Metadata-only, whichever models have credentials
concord -o ./out findings.sarif

# Shared-context (default): one explorer reads the repo, all voters share the brief
concord --srcroot /path/to/repo -o ./out findings.sarif

# Per-model: each model crawls the repo itself
concord --srcroot /path/to/repo --context-strategy per-model -o ./out findings.sarif

# Parse and normalize findings only, no model calls
concord --dry-run findings.csv
```

Run `concord --help` for all flags.

## Extra architecture context (`--context-dir`)

`--srcroot` lets the models read the finding's own repo. But whether a finding
is actually exploitable often depends on code that lives somewhere else, an API
gateway that strips or rewrites a header, an auth service that validates a token,
a shared library in a sibling repo, a network policy that blocks a route. Without
that surrounding code a model has to guess, which is where both false positives
(flagging something a gateway already neutralizes) and false negatives (clearing
something that a downstream service actually trusts) come from.

`--context-dir` points the agents at additional read-only roots so they can
follow the real request path across repos instead of guessing. It is repeatable,
requires `--srcroot`, and each root is sandboxed like `--srcroot` is. The models
do not read everything up front, they get a short inventory of what exists and
open files on demand, so adding a large context set is cheap.

```bash
concord --srcroot /path/to/app \
  --context-dir /path/to/api-gateway \
  --context-dir /path/to/auth-service \
  -o ./out findings.sarif
```

A big context set is reachable but not discoverable, so a short human-written map
helps the models know where to look. Put a `TRIAGE_CONTEXT.md` at the top of a
context root (it is picked up automatically) or pass one with `--context-guide`.
It should say, in paths relative to the context roots, where the gateway,
services, and policies live.

## Sharing context bundles (export/import)

Assembling that set of repos and docs by hand on every machine is the tedious
part, especially in CI or for a teammate who does not have all the repos cloned.
`concord context export` and `context import` move a whole context directory
around as a single `.tar.gz`.

Export scans a context directory one level deep. For each git repo it records
the `origin` remote URL and the exact commit SHA in a JSON manifest rather than
copying the tree, and it packs loose documents (guides, diagrams, notes)
directly. Repos with no remote origin are skipped, since they cannot be
re-cloned, and hidden directories are ignored so caches and stray VCS metadata
never leak in. Every repo and file is listed as it is processed.

```bash
concord context export --context-dir ./context -o context-bundle.tar.gz
```

Import re-clones each recorded repo (shallow and pinned to the manifest's commit
by default, `--latest` for current HEAD, `--deep-clone` for full history) and
restores the loose files, reproducing the same layout. A repo that fails to clone
(permissions, network) becomes a warning instead of aborting the import.

```bash
concord context import context-bundle.tar.gz -o ./context
```

Then point `--context-dir` at the restored directory (or its subdirectories) for
the triage run.

## Credentials

Each provider is used only if its credential is present; missing ones are
skipped, and at least one is required.

- Claude, `ANTHROPIC_API_KEY` (or `--api-key`)
- Gemini, `GOOGLE_API_KEY` (or `--google-api-key`)
- Codex, `OPENAI_API_KEY` (or `--codex-api-key`)
- Azure, `AZURE_OPENAI_API_KEY` + `AZURE_OPENAI_ENDPOINT` (or `--azure-*` flags)

Claude can also run through Amazon Bedrock with `--bedrock` (auto-enabled when
`CLAUDE_CODE_USE_BEDROCK` or `AWS_BEARER_TOKEN_BEDROCK` is set). Auth flows
through the AWS credential chain, a Bedrock API key in `AWS_BEARER_TOKEN_BEDROCK`
takes precedence, otherwise the SigV4 chain (`AWS_PROFILE` / SSO / env / IAM
role). A region is required. The model is a Bedrock/inference-profile ID via
`--bedrock-model`.

## Status

Experimental. Phases 1 through 5 of the design are implemented, ingest, the
agentic tool loop, the quad-model vote and adjudication, all five input
formats, both report writers, and goreleaser packaging to GitHub releases.

`--effort` (low/medium/high) is mapped to real reasoning parameters on the
single-shot paths (voters and adjudication), Claude extended thinking and an
OpenAI/Azure `reasoning_effort`. For Claude the request is built by an
`anthropic-sdk-go` adapter that picks the right thinking API per model,
`thinking:{type:"adaptive"}` + `output_config.effort` on current models
(Opus 4.6+, Sonnet 5, Fable 5), the legacy `budget_tokens` on older ones, and no
thinking on models that lack it. On the agentic paths effort controls the
tool-iteration budget instead, because multi-turn tool loops cannot faithfully
round-trip Claude thinking blocks.

Anthropic prompt caching is on by default on both the direct and Bedrock Claude
paths (two `cache_control` breakpoints per request, 5m TTL); disable nothing, it
silently no-ops below a model's minimum cacheable prefix. Known follow-up, the
Gemini thinking budget is not honored by langchaingo v0.1.14 (the mode is passed
but ignored).
