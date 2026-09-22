package main

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/charmbracelet/lipgloss"
	"github.com/spf13/cobra"
)

// The configure subcommand generates a harmonia.toml from configuration the
// user already has: credential environment variables and the provider
// settings of coding agents they already run (opencode, pi). It makes a
// first-time run work without hand-writing provider config.

var (
	confSrcStyle   = lipgloss.NewStyle().Foreground(lipgloss.Color("44"))  // source paths, cyan
	confModelStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("42"))  // model names, green
	confWarnStyle  = lipgloss.NewStyle().Foreground(lipgloss.Color("214")) // warnings, amber
	confDimStyle   = lipgloss.NewStyle().Foreground(lipgloss.Color("245")) // notes, dim
)

// detectedModel is one [[models]] entry configure can generate.
type detectedModel struct {
	name          string
	protocol      string // openai | anthropic | gemini | azure
	endpoint      string
	model         string
	apiKey        string // "", "env:NAME", or a literal key
	contextWindow int    // 0 = omit (preset default applies)
	windowAssumed bool
	priceIn       float64
	priceOut      float64
	apiVersion    string // azure protocol only
	source        string // provenance note for the generated comment
	presetMerge   bool   // entry intentionally merges with a built-in preset
	hasSecret     bool   // apiKey is a literal value
}

// configureOpts carries everything runConfigure needs, injected so tests can
// point at temporary directories and fake environments.
type configureOpts struct {
	output    string
	force     bool
	printOnly bool
	noSecrets bool
	getEnv    func(string) (string, bool)
	homeDir   string
	cwd       string
	now       func() time.Time
	out       io.Writer
}

func newConfigureCmd() *cobra.Command {
	var o configureOpts
	cmd := &cobra.Command{
		Use:     "configure",
		Aliases: []string{"init"},
		Short:   "Detect LLM provider config and write a harmonia.toml",
		Long: `Detect LLM provider configuration from your environment and the
coding agents you already use, and write a harmonia.toml.

Sources, in order:
  1. Environment variables — ANTHROPIC_API_KEY, OPENAI_API_KEY,
     GOOGLE_API_KEY, AZURE_OPENAI_* — become preset entries that reference
     the variable with an env: pointer; the key itself is never copied.
  2. opencode — opencode.json or opencode.jsonc in the current directory,
     then ~/.config/opencode/ — custom provider endpoints, models, and keys.
  3. pi — models.json and auth.json in $PI_CODING_AGENT_DIR or
     ~/.pi/agent — custom providers, models, and keys.

The file is written to ./harmonia.toml (or --output) and is never
overwritten without --force. Literal API keys found in source configs are
written into the file; use --no-secrets to omit them and print export
lines instead.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			if o.getEnv == nil {
				o.getEnv = os.LookupEnv
			}
			home, err := os.UserHomeDir()
			if err != nil {
				return fmt.Errorf("resolve home directory: %w", err)
			}
			o.homeDir = home
			cwd, err := os.Getwd()
			if err != nil {
				return fmt.Errorf("resolve working directory: %w", err)
			}
			o.cwd = cwd
			if o.now == nil {
				o.now = time.Now
			}
			if o.out == nil {
				o.out = cmd.OutOrStdout()
			}
			return runConfigure(o)
		},
	}
	f := cmd.Flags()
	f.StringVar(&o.output, "output", "", "path to write (default ./harmonia.toml)")
	f.BoolVar(&o.force, "force", false, "overwrite an existing harmonia.toml")
	f.BoolVarP(&o.printOnly, "print", "p", false, "print the generated file instead of writing it")
	f.BoolVar(&o.noSecrets, "no-secrets", false, "never write literal API keys; print export lines instead")
	return cmd
}

func runConfigure(o configureOpts) error {
	if o.output == "" {
		o.output = filepath.Join(o.cwd, "harmonia.toml")
	}

	envModels, envNotes, envVars := detectEnvModels(o.getEnv)
	ocModels, ocNotes, ocSrc := detectOpencodeModels(o.homeDir, o.cwd, o.getEnv)
	piModels, piNotes, piSrc := detectPiModels(o.homeDir, o.getEnv)
	src := sources(envVars, ocSrc, piSrc)

	models := append([]detectedModel{}, envModels...)
	models = append(models, ocModels...)
	models = append(models, piModels...)
	finalizeNames(models)
	notes := append(append(envNotes, ocNotes...), piNotes...)

	if len(models) == 0 {
		fmt.Fprintln(o.out, confWarnStyle.Render("No LLM provider configuration found."))
		fmt.Fprintln(o.out, confDimStyle.Render("Set a credential variable (ANTHROPIC_API_KEY, OPENAI_API_KEY, GOOGLE_API_KEY),"))
		fmt.Fprintln(o.out, confDimStyle.Render("configure a provider in opencode (~/.config/opencode/opencode.json), or in pi"))
		fmt.Fprintln(o.out, confDimStyle.Render("(~/.pi/agent/models.json), then re-run `harmonia configure`."))
		return nil
	}

	seen := map[string]bool{}
	var dedupNotes []string
	filtered := models[:0]
	for _, m := range models {
		k := m.protocol + "\x00" + m.endpoint + "\x00" + m.model
		if seen[k] {
			dedupNotes = append(dedupNotes, fmt.Sprintf("skipped %s: duplicate of an earlier model from %s", m.name, m.source))
			continue
		}
		seen[k] = true
		filtered = append(filtered, m)
	}
	models = filtered
	notes = append(notes, dedupNotes...)

	content, exports := renderTOML(models, src, o.noSecrets, o.now)

	if o.printOnly {
		fmt.Fprint(o.out, content)
		for _, e := range exports {
			fmt.Fprintln(o.out, e)
		}
		return nil
	}

	if _, err := os.Stat(o.output); err == nil && !o.force {
		return fmt.Errorf("%s already exists — pass --force to overwrite, or edit it in place", o.output)
	}
	if err := os.WriteFile(o.output, []byte(content), 0o644); err != nil {
		return fmt.Errorf("write %s: %w", o.output, err)
	}

	printConfigureSummary(o.out, o.output, models, envNotes, src, notes, exports, o.noSecrets)
	return nil
}

// sources builds the one-line-per-source list for the header comment and the
// summary.
func sources(envVars []string, ocSrc, piSrc string) []string {
	var s []string
	if len(envVars) > 0 {
		s = append(s, "environment: "+strings.Join(envVars, ", "))
	}
	if ocSrc != "" {
		s = append(s, "opencode: "+ocSrc)
	}
	if piSrc != "" {
		s = append(s, "pi: "+piSrc)
	}
	return s
}

// detectEnvModels turns credential environment variables into preset-merge
// entries. Keys are referenced, never copied. It returns the models, any
// notes, and the variable names it found.
func detectEnvModels(getEnv func(string) (string, bool)) ([]detectedModel, []string, []string) {
	var models []detectedModel
	var notes []string
	var vars []string

	if v, ok := getEnv("ANTHROPIC_API_KEY"); ok && v != "" {
		vars = append(vars, "ANTHROPIC_API_KEY")
		models = append(models, detectedModel{
			name: "claude", protocol: "anthropic",
			apiKey: "env:ANTHROPIC_API_KEY", presetMerge: true,
			source: "environment (ANTHROPIC_API_KEY)",
		})
	}
	if v, ok := getEnv("OPENAI_API_KEY"); ok && v != "" {
		vars = append(vars, "OPENAI_API_KEY")
		models = append(models, detectedModel{
			name: "openai", protocol: "openai",
			apiKey: "env:OPENAI_API_KEY", presetMerge: true,
			source: "environment (OPENAI_API_KEY)",
		})
	}
	if v, ok := getEnv("GOOGLE_API_KEY"); ok && v != "" {
		vars = append(vars, "GOOGLE_API_KEY")
		models = append(models, detectedModel{
			name: "gemini", protocol: "gemini",
			apiKey: "env:GOOGLE_API_KEY", presetMerge: true,
			source: "environment (GOOGLE_API_KEY)",
		})
	}
	if _, ok := getEnv("AZURE_OPENAI_API_KEY"); ok {
		if endpoint, ok := getEnv("AZURE_OPENAI_ENDPOINT"); ok && endpoint != "" {
			vars = append(vars, "AZURE_OPENAI_API_KEY")
			m := detectedModel{
				name: "azure", protocol: "azure",
				apiKey: "env:AZURE_OPENAI_API_KEY", presetMerge: true,
				endpoint: endpoint, source: "environment (AZURE_OPENAI_*)",
			}
			if v, ok := getEnv("AZURE_OPENAI_API_VERSION"); ok && v != "" {
				m.apiVersion = v
			}
			models = append(models, m)
		} else {
			notes = append(notes, "AZURE_OPENAI_API_KEY is set but AZURE_OPENAI_ENDPOINT is missing; the azure preset needs both")
		}
	}
	if v, ok := getEnv("CLAUDE_CODE_USE_BEDROCK"); ok && v != "" {
		notes = append(notes, "CLAUDE_CODE_USE_BEDROCK detected: the claude preset will run via Bedrock (a region is still required)")
	} else if v, ok := getEnv("AWS_BEARER_TOKEN_BEDROCK"); ok && v != "" {
		notes = append(notes, "AWS_BEARER_TOKEN_BEDROCK detected: the claude preset will run via Bedrock (a region is still required)")
	}
	return models, notes, vars
}

// detectOpencodeModels reads opencode's config (project-local first, then
// global) and maps its custom providers to harmonia model entries.
func detectOpencodeModels(home, cwd string, getEnv func(string) (string, bool)) ([]detectedModel, []string, string) {
	path := firstExisting(
		filepath.Join(cwd, "opencode.json"),
		filepath.Join(cwd, "opencode.jsonc"),
		filepath.Join(home, ".config", "opencode", "opencode.json"),
		filepath.Join(home, ".config", "opencode", "opencode.jsonc"),
	)
	if path == "" {
		return nil, nil, ""
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, []string{fmt.Sprintf("opencode: read %s: %v", path, err)}, path
	}
	var doc struct {
		Provider map[string]struct {
			NPM     string `json:"npm"`
			Options struct {
				BaseURL string `json:"baseURL"`
				APIKey  string `json:"apiKey"`
			} `json:"options"`
			Models map[string]struct {
				ContextWindow int `json:"contextWindow"`
				Limit         struct {
					Context int `json:"context"`
				} `json:"limit"`
			} `json:"models"`
		} `json:"provider"`
	}
	if err := json.Unmarshal(stripJSONC(data), &doc); err != nil {
		return nil, []string{fmt.Sprintf("opencode: parse %s: %v", path, err)}, path
	}
	var ids []string
	for id := range doc.Provider {
		ids = append(ids, id)
	}
	sort.Strings(ids)

	var models []detectedModel
	var notes []string
	for _, id := range ids {
		p := doc.Provider[id]
		protocol, ok := opencodeProtocol(id, p.NPM)
		if !ok {
			notes = append(notes, fmt.Sprintf("opencode: provider %q not supported (npm %q); add it to harmonia.toml manually", id, p.NPM))
			continue
		}
		endpoint := p.Options.BaseURL
		if id == "openrouter" && endpoint == "" {
			endpoint = "https://openrouter.ai/api/v1"
		}
		var mid []string
		for m := range p.Models {
			mid = append(mid, m)
		}
		sort.Strings(mid)
		for _, m := range mid {
			ctx := p.Models[m].Limit.Context
			if ctx == 0 {
				ctx = p.Models[m].ContextWindow
			}
			windowAssumed := false
			if ctx == 0 {
				ctx = 128000
				windowAssumed = true
			}
			key := p.Options.APIKey
			models = append(models, detectedModel{
				name:          slugName(id + "-" + m),
				protocol:      protocol,
				endpoint:      endpoint,
				model:         m,
				apiKey:        key,
				hasSecret:     key != "",
				contextWindow: ctx,
				windowAssumed: windowAssumed,
				source:        fmt.Sprintf("opencode provider %q in %s", id, path),
			})
		}
	}
	return models, notes, path
}

// opencodeProtocol maps an opencode provider to a harmonia protocol.
func opencodeProtocol(id, npm string) (string, bool) {
	switch npm {
	case "@ai-sdk/openai-compatible", "@ai-sdk/openai":
		return "openai", true
	case "@ai-sdk/anthropic":
		return "anthropic", true
	}
	switch id {
	case "openai", "openrouter":
		return "openai", true
	case "anthropic":
		return "anthropic", true
	}
	return "", false
}

// detectPiModels reads pi's models.json (provider definitions) and auth.json
// (credentials) from the pi agent directory.
func detectPiModels(home string, getEnv func(string) (string, bool)) ([]detectedModel, []string, string) {
	agentDir := getEnvOr(getEnv, "PI_CODING_AGENT_DIR", filepath.Join(home, ".pi", "agent"))
	modelsPath := filepath.Join(agentDir, "models.json")
	if _, err := os.Stat(modelsPath); err != nil {
		return nil, nil, ""
	}
	data, err := os.ReadFile(modelsPath)
	if err != nil {
		return nil, []string{fmt.Sprintf("pi: read %s: %v", modelsPath, err)}, modelsPath
	}
	var doc struct {
		Providers map[string]struct {
			BaseURL string `json:"baseUrl"`
			APIKey  string `json:"apiKey"`
			Models  []struct {
				ID            string `json:"id"`
				ContextWindow int    `json:"contextWindow"`
				Cost          struct {
					Input  float64 `json:"input"`
					Output float64 `json:"output"`
				} `json:"cost"`
			} `json:"models"`
		} `json:"providers"`
	}
	if err := json.Unmarshal(stripJSONC(data), &doc); err != nil {
		return nil, []string{fmt.Sprintf("pi: parse %s: %v", modelsPath, err)}, modelsPath
	}

	auth := map[string]struct {
		Type string            `json:"type"`
		Key  string            `json:"key"`
		Env  map[string]string `json:"env"`
	}{}
	if authData, err := os.ReadFile(filepath.Join(agentDir, "auth.json")); err == nil {
		_ = json.Unmarshal(stripJSONC(authData), &auth)
	}

	var models []detectedModel
	var notes []string
	var ids []string
	for id := range doc.Providers {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	for _, id := range ids {
		p := doc.Providers[id]
		key, hasSecret := piProviderKey(id, auth)
		for _, m := range p.Models {
			ctx := m.ContextWindow
			windowAssumed := false
			if ctx == 0 {
				ctx = 128000
				windowAssumed = true
			}
			models = append(models, detectedModel{
				name:          slugName(id + "-" + m.ID),
				protocol:      "openai",
				endpoint:      p.BaseURL,
				model:         m.ID,
				apiKey:        key,
				hasSecret:     hasSecret,
				contextWindow: ctx,
				windowAssumed: windowAssumed,
				priceIn:       m.Cost.Input,
				priceOut:      m.Cost.Output,
				source:        fmt.Sprintf("pi provider %q in %s", id, modelsPath),
			})
		}
	}
	return models, notes, modelsPath
}

// piProviderKey resolves a pi provider's credential: a literal key from
// auth.json, an env: pointer to the variable pi itself uses, or nothing.
func piProviderKey(id string, auth map[string]struct {
	Type string            `json:"type"`
	Key  string            `json:"key"`
	Env  map[string]string `json:"env"`
}) (string, bool) {
	c, ok := auth[id]
	if !ok || c.Type != "api_key" {
		return "", false
	}
	if c.Key != "" {
		return c.Key, true
	}
	var names []string
	for n := range c.Env {
		names = append(names, n)
	}
	if len(names) > 0 {
		sort.Strings(names)
		return "env:" + names[0], false
	}
	return "", false
}

var (
	reservedNames = map[string]bool{"adjudicator": true, "strict": true, "business": true, "codeflow": true}
	presetNames   = map[string]bool{"claude": true, "gemini": true, "openai": true, "azure": true}
)

// finalizeNames sanitizes generated names and resolves collisions.
func finalizeNames(models []detectedModel) {
	used := map[string]bool{}
	for i := range models {
		m := &models[i]
		name := slugName(m.name)
		if name == "" {
			name = "model"
		}
		if !m.presetMerge && (reservedNames[name] || presetNames[name]) {
			name += "-custom"
		}
		for used[name] {
			name += "-2"
		}
		used[name] = true
		m.name = name
	}
}

// slugName lowers s and maps every run of non [a-z0-9-] characters to '-'.
func slugName(s string) string {
	s = strings.ToLower(s)
	var b strings.Builder
	lastDash := false
	for _, r := range s {
		switch {
		case (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '-':
			b.WriteRune(r)
			lastDash = r == '-'
		default:
			if !lastDash {
				b.WriteByte('-')
				lastDash = true
			}
		}
	}
	return strings.Trim(b.String(), "-")
}

// renderTOML renders the generated file. It also returns the export lines
// for secrets omitted under --no-secrets.
func renderTOML(models []detectedModel, sources []string, noSecrets bool, now func() time.Time) (string, []string) {
	var b strings.Builder
	fmt.Fprintf(&b, "# harmonia.toml — generated by `harmonia configure` on %s\n", now().Format("2006-01-02 15:04:05"))
	for _, s := range sources {
		fmt.Fprintf(&b, "# source: %s\n", s)
	}
	b.WriteString("\n# Each [[models]] entry becomes one voter; the first model is the\n")
	b.WriteString("# preferred one (explorer, default judge, analyst base).\n")
	b.WriteString("# See the README \"Configuration\" section for every key.\n\n")
	for _, m := range models {
		b.WriteString("[[models]]\n")
		fmt.Fprintf(&b, "# %s\n", m.source)
		fmt.Fprintf(&b, "name = %s\n", strconv.Quote(m.name))
		fmt.Fprintf(&b, "protocol = %s\n", strconv.Quote(m.protocol))
		if m.endpoint != "" {
			fmt.Fprintf(&b, "endpoint = %s\n", strconv.Quote(m.endpoint))
		}
		if m.apiKey != "" && !(noSecrets && m.hasSecret) {
			fmt.Fprintf(&b, "api_key = %s\n", strconv.Quote(m.apiKey))
		}
		if m.model != "" {
			fmt.Fprintf(&b, "model = %s\n", strconv.Quote(m.model))
		}
		if m.contextWindow > 0 {
			line := fmt.Sprintf("context_window = %d", m.contextWindow)
			if m.windowAssumed {
				line += " # assumed; no context window in source"
			}
			b.WriteString(line + "\n")
		}
		if m.apiVersion != "" {
			fmt.Fprintf(&b, "api_version = %s\n", strconv.Quote(m.apiVersion))
		}
		if m.priceIn > 0 && m.priceOut > 0 {
			fmt.Fprintf(&b, "price_in = %s\n", strconv.FormatFloat(m.priceIn, 'f', -1, 64))
			fmt.Fprintf(&b, "price_out = %s\n", strconv.FormatFloat(m.priceOut, 'f', -1, 64))
		}
		b.WriteString("\n")
	}
	var exports []string
	if noSecrets {
		for _, m := range models {
			if m.hasSecret {
				vars := envVarFor(m.name)
				exports = append(exports, fmt.Sprintf("# key for %s — add  api_key = \"env:HARMONIA_KEY_%s\"  to that entry:", m.name, vars))
				exports = append(exports, fmt.Sprintf("export HARMONIA_KEY_%s=%s", vars, strconv.Quote(m.apiKey)))
			}
		}
	}
	return b.String(), exports
}

// envVarFor renders a name as a valid shell environment variable name.
func envVarFor(name string) string {
	return strings.ReplaceAll(strings.ToUpper(slugName(name)), "-", "_")
}

func printConfigureSummary(w io.Writer, path string, models []detectedModel, envNotes []string, sources []string, notes []string, exports []string, noSecrets bool) {
	fmt.Fprintln(w, "Sources:")
	for _, s := range sources {
		fmt.Fprintf(w, "  %s %s\n", confSrcStyle.Render("•"), s)
	}
	for _, n := range envNotes {
		fmt.Fprintf(w, "  %s %s\n", confSrcStyle.Render("•"), n)
	}

	fmt.Fprintf(w, "\n%s (%d models):\n", confModelStyle.Render(path), len(models))
	width := 0
	for _, m := range models {
		if len(m.name) > width {
			width = len(m.name)
		}
	}
	for _, m := range models {
		fmt.Fprintf(w, "  %s %s %-11s %s\n",
			confModelStyle.Render(m.name),
			strings.Repeat(" ", width-len(m.name)),
			m.protocol,
			modelDetail(m))
	}

	if len(notes) > 0 {
		fmt.Fprintln(w, "\nSkipped / notes:")
		for _, n := range notes {
			fmt.Fprintf(w, "  %s %s\n", confDimStyle.Render("•"), n)
		}
	}

	if repoRoot, hasSecrets := repoWithSecrets(path, models); !noSecrets && hasSecrets {
		fmt.Fprintln(w, "")
		fmt.Fprintf(w, "  %s %s\n", confWarnStyle.Render("!"),
			fmt.Sprintf("%s contains literal API keys and lives in git repo %s — do not commit it.", filepath.Base(path), repoRoot))
		fmt.Fprintf(w, "  %s echo \"%s\" >> %s/.gitignore\n", confWarnStyle.Render(">"), filepath.Base(path), repoRoot)
	}

	if len(exports) > 0 {
		fmt.Fprintln(w, "\nKeys not written (--no-secrets); add these to your shell profile:")
		for _, e := range exports {
			fmt.Fprintf(w, "  %s\n", confDimStyle.Render(e))
		}
	}

	fmt.Fprintln(w, "")
	fmt.Fprintf(w, "%s\n", confDimStyle.Render("Next: harmonia <input> --srcroot <repo>   (or edit "+filepath.Base(path)+" to tune the model set)"))
}

func modelDetail(m detectedModel) string {
	var parts []string
	if m.endpoint != "" {
		parts = append(parts, m.endpoint)
	}
	if m.apiKey != "" {
		if strings.HasPrefix(m.apiKey, "env:") {
			parts = append(parts, "key: "+strings.TrimPrefix(m.apiKey, "env:"))
		} else {
			parts = append(parts, "key: ***")
		}
	}
	if m.model != "" {
		parts = append(parts, "model: "+m.model)
	}
	if m.contextWindow > 0 {
		parts = append(parts, fmt.Sprintf("%d ctx", m.contextWindow))
	}
	return strings.Join(parts, "  ")
}

// repoWithSecrets walks up from path looking for a git repo; it returns the
// repo root if the file is inside one and at least one model carries a
// literal key.
func repoWithSecrets(path string, models []detectedModel) (string, bool) {
	hasSecrets := false
	for _, m := range models {
		if m.hasSecret {
			hasSecrets = true
			break
		}
	}
	if !hasSecrets {
		return "", false
	}
	dir := filepath.Dir(path)
	for {
		if _, err := os.Stat(filepath.Join(dir, ".git")); err == nil {
			return dir, true
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", false
		}
		dir = parent
	}
}

func firstExisting(paths ...string) string {
	for _, p := range paths {
		if _, err := os.Stat(p); err == nil {
			return p
		}
	}
	return ""
}

func getEnvOr(getEnv func(string) (string, bool), key, def string) string {
	if v, ok := getEnv(key); ok && v != "" {
		return v
	}
	return def
}

// stripJSONC removes // line comments and /* */ block comments outside of
// string literals, so standard JSON parsing accepts opencode.jsonc and pi's
// models.json (both of which allow comments).
func stripJSONC(data []byte) []byte {
	var b strings.Builder
	inString := false
	escaped := false
	i := 0
	for i < len(data) {
		c := data[i]
		if inString {
			b.WriteByte(c)
			switch {
			case escaped:
				escaped = false
			case c == '\\':
				escaped = true
			case c == '"':
				inString = false
			}
			i++
			continue
		}
		switch {
		case c == '"':
			inString = true
			b.WriteByte(c)
			i++
		case c == '/' && i+1 < len(data) && data[i+1] == '/':
			for i < len(data) && data[i] != '\n' {
				i++
			}
		case c == '/' && i+1 < len(data) && data[i+1] == '*':
			i += 2
			for i+1 < len(data) && !(data[i] == '*' && data[i+1] == '/') {
				i++
			}
			i += 2
		default:
			b.WriteByte(c)
			i++
		}
	}
	return []byte(b.String())
}
