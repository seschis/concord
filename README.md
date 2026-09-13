# concord

![Go](https://img.shields.io/badge/go-1.26%2B-00ADD8)
![Release](https://img.shields.io/github/v/release/seschis/concord)
![License](https://img.shields.io/badge/License-Apache--2.0-blue)

`concord` is a Go CLI that triages security scanner findings with multiple LLMs.
It ingests scanner output (SARIF, JSON, CSV, Markdown, XLSX), lets the models
read the flagged source with sandboxed file tools, asks up to four models to
judge each finding independently, takes a majority vote, and adjudicates ties
by having Claude review every analysis.

It reads and writes local files only. No backend, no LLM proxy, no telemetry.

## Why multi-model voting

A single model's verdict on a finding is unstable: it depends on the model, the
prompt, and the order in which evidence was read. `concord` treats triage as an
ensemble problem instead:

- Up to four models (Claude, Gemini, Codex/direct OpenAI, Azure OpenAI) judge
  each finding independently.
- If two or more voters agree on a category (real, not-real, needs-more-context),
  the most conservative verdict in that category stands: when the vote splits,
  the finding is treated as more real, never as safe (`CONFIRMED_REAL` over
  `LIKELY_REAL`, `UNLIKELY` over `NOT_EXPLOITABLE`).
- If the vote ties, Claude (or the first available model) reviews every
  analysis and issues a final verdict with the deciding factor.
- A model that fails or times out drops out of the vote rather than aborting
  the finding.

## What it does

- Four-model triage with a majority vote and Claude-preferred adjudication.
- On-demand, tool-calling source reading, sandboxed to `--srcroot`.
- Two context strategies via `--context-strategy`: `shared` (one explorer
  reads the repo, all voters share the brief, cheaper) and `per-model` (each
  model crawls).
- Live TUI dashboard on interactive terminals; `--plain` for plain output.
- Per-finding agentic transcripts (every tool call with its args and result,
  JSONL) in `<output-dir>/transcripts/`.
- Live cost accounting: every model call is priced and the total accrues on
  screen as the run proceeds.
- Inputs, SARIF, JSON, CSV, Markdown, XLSX.
- Outputs, `results.json` (with a `concordAnalysis` block per finding) and
  a human-readable `report.md`.

## How a run looks

![concord live TUI](assets/demo.gif)

The same run writes a `report.md` with the per-model verdicts, reasoning, and
cost breakdown. A trimmed excerpt from the demo run:

```markdown
# Security Scan Triage Report

**Date:** 2026-09-13 14:28:41
**Input file:** `examples/findings.sarif`
**Models:** Claude (Bedrock) (us.anthropic.claude-sonnet-4-5-20250929-v1:0)
**Context strategy:** shared (srcroot `examples/sample-app`)
**Effort:** low
**Findings analysed:** 2
**Total cost:** $0.1369

## Summary

- 🔴 CONFIRMED_REAL: 1
- 🟠 LIKELY_REAL: 0
- 🟡 UNLIKELY: 0
- 🟢 NOT_EXPLOITABLE: 1
- 🔵 NEEDS_MORE_CONTEXT: 0

---

## 🔴 F001 — Command injection

**File:** `main.go` line 30
**Severity:** HIGH  |  **CWE:** CWE-78  |  **CVSS 4.0:** 9.3 Critical
**CVSS vector:** `CVSS:4.0/AV:N/AC:L/AT:N/PR:N/UI:N/VC:H/VI:H/VA:H/SC:L/SI:L/SA:N`
**Final verdict:** CONFIRMED_REAL  (agreement: single)
**Classification:** TRUE_POSITIVE

Confirmed command injection in demo training app—exploitable if deployed but
intentionally vulnerable by design; educate on demo code isolation rather than
patching.

### Analysis

**Exploit scenario:** Attacker sends `GET /ping?host=example.com;whoami`. The
shell interprets metacharacters, executing both ping and the injected command.
No authentication required, works on first attempt, grants shell access with
service process privileges.

**Triage reasoning:** Textbook command injection with direct, unauthenticated
network reachability. User input flows unvalidated into `sh -c`. Zero
mitigations present (contrast with the `/users` endpoint, which has regex
validation).

**Counter-argument considered:** What if this demo code was accidentally
copied into a production service? Demo code has a history of escaping into
real environments.

**Why the counter-argument fails:** The challenge is valid, but the verdict
stands: the vulnerability is confirmed real and exploitable if deployed. The
demo context affects prioritization, not the technical verdict.

| Role | Verdict | Cost | Notes |
|---|---|---|---|
| explorer | — | $0.0232 | shared context gathering |
| claude (voter) | CONFIRMED_REAL | $0.0479 | direct, unvalidated input to `sh -c`; no mitigations |

## 🟢 F002 — SQL injection

**File:** `users.go` line 22
**Severity:** MEDIUM  |  **CWE:** CWE-89  |  **CVSS 4.0:** 0.0 None
**Final verdict:** NOT_EXPLOITABLE  (agreement: single)
**Classification:** FALSE_POSITIVE

SQL injection via string interpolation in `users.go:22` is NOT exploitable
due to strict `^[0-9,]+$` regex validation preventing all SQL metacharacters,
though parameterized queries should replace this anti-pattern.

### Analysis

**Counter-argument considered:** Could the regex validation be bypassed via
encoding tricks, null bytes, or multi-line attacks?

**Why the counter-argument fails:** Go's regex engine handles `^[0-9,]+$`
unambiguously — the anchors prevent partial matches, and validation happens
before the SQL construction with an early return on failure. No encoding,
Unicode normalization, or null byte attack can bypass it.
```

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

# The built-in demo: two findings against examples/sample-app
concord --srcroot examples/sample-app -o ./demo-out --effort low examples/findings.sarif
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

## Cost and caching

Every model call is priced against a per-model rate card (Claude cached input is
priced separately from normal input), so the total accrues live in the TUI and
lands in the report as a per-model breakdown. Anthropic prompt caching is on by
default on both the direct and Bedrock Claude paths; it silently no-ops below a
model's minimum cacheable prefix. On the agentic paths,
`--enable-context-pruning` trims older large tool results from the re-sent
context to cut explorer cost.

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

Experimental. Everything in the [design](DESIGN.md) is implemented: ingest for
all five formats, the agentic tool loop, the quad-model vote and adjudication,
both report writers, context bundles, and goreleaser publishing to GitHub
releases. Known gap: Gemini's `thinking_budget` is passed to langchaingo
v0.1.14 but not honored by it (see [DESIGN.md](DESIGN.md)).

## License

Apache 2.0 — see [LICENSE](LICENSE).
