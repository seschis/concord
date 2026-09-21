package provider

import (
	"errors"
	"fmt"
	"os"
	"regexp"
	"strings"

	"github.com/BurntSushi/toml"

	"github.com/seschis/harmonia/internal/triage"
)

// modelTable is one [[models]] entry of harmonia.toml. Every field is optional
// at decode time; a spec's final values only exist after the preset/file/flag
// merge, so field-level rules are enforced by ValidateSpecs on the merged set.
type modelTable struct {
	Name          string   `toml:"name"`
	Protocol      string   `toml:"protocol"`
	Endpoint      string   `toml:"endpoint"`
	APIKey        string   `toml:"api_key"`
	Model         string   `toml:"model"`
	ContextWindow int      `toml:"context_window"`
	PriceIn       *float64 `toml:"price_in"`
	PriceOut      *float64 `toml:"price_out"`
	Bedrock       bool     `toml:"bedrock"`
	Region        string   `toml:"region"`
	APIVersion    string   `toml:"api_version"`
}

type modelConfig struct {
	Models []modelTable `toml:"models"`
}

// LoadTOML reads a harmonia.toml model config. Decoding is strict: any unknown
// key (top level or inside a [[models]] table) is an error, so typos fail at
// load instead of silently vanishing. A duplicate name within one file is an
// error (typos deserve failure, not last-wins); repeated names ACROSS layers
// are redefinitions and merge instead. The returned specs carry only the
// fields the file sets, in declaration order.
func LoadTOML(path string) ([]ModelSpec, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var doc modelConfig
	md, err := toml.Decode(string(data), &doc)
	if err != nil {
		return nil, fmt.Errorf("parse %s: %w", path, err)
	}
	if undecoded := md.Undecoded(); len(undecoded) > 0 {
		keys := make([]string, len(undecoded))
		for i, k := range undecoded {
			keys[i] = k.String()
		}
		return nil, fmt.Errorf("parse %s: unknown field(s) %s", path, strings.Join(keys, ", "))
	}
	specs := make([]ModelSpec, 0, len(doc.Models))
	seen := make(map[string]bool, len(doc.Models))
	for _, m := range doc.Models {
		if m.Name != "" {
			if seen[m.Name] {
				return nil, fmt.Errorf("%s: duplicate model name %q", path, m.Name)
			}
			seen[m.Name] = true
		}
		specs = append(specs, ModelSpec{
			Name:          m.Name,
			Protocol:      Protocol(m.Protocol),
			Endpoint:      m.Endpoint,
			APIKey:        m.APIKey,
			Model:         m.Model,
			ContextWindow: m.ContextWindow,
			PriceIn:       m.PriceIn,
			PriceOut:      m.PriceOut,
			Bedrock:       m.Bedrock,
			Region:        m.Region,
			APIVersion:    m.APIVersion,
		})
	}
	return specs, nil
}

// mergeSpec overlays one spec's set fields onto another: a field counts as
// set when it is non-zero (non-empty string, positive window, non-nil price,
// bedrock=true), and zero values never clear a lower layer.
func mergeSpec(base, overlay ModelSpec) ModelSpec {
	if overlay.Protocol != "" {
		base.Protocol = overlay.Protocol
	}
	if overlay.Endpoint != "" {
		base.Endpoint = overlay.Endpoint
	}
	if overlay.APIKey != "" {
		base.APIKey = overlay.APIKey
	}
	if overlay.Model != "" {
		base.Model = overlay.Model
	}
	if overlay.ContextWindow > 0 {
		base.ContextWindow = overlay.ContextWindow
	}
	if overlay.PriceIn != nil {
		base.PriceIn = overlay.PriceIn
	}
	if overlay.PriceOut != nil {
		base.PriceOut = overlay.PriceOut
	}
	if overlay.Bedrock {
		base.Bedrock = true
	}
	if overlay.Region != "" {
		base.Region = overlay.Region
	}
	if overlay.APIVersion != "" {
		base.APIVersion = overlay.APIVersion
	}
	return base
}

// MergeSpecs merges spec layers in ascending priority: earlier arguments are
// the lower layers, and per field the highest layer that sets a field wins.
// The result keeps each name's first-seen position across the layers — presets
// passed first keep the preset order, custom names follow in declaration
// order — and a name repeated within one layer is last-wins. Nameless specs
// pass through unmerged (ValidateSpecs rejects them).
func MergeSpecs(layers ...[]ModelSpec) []ModelSpec {
	var order []string
	byName := map[string]ModelSpec{}
	var nameless []ModelSpec
	for _, layer := range layers {
		for _, s := range layer {
			if s.Name == "" {
				nameless = append(nameless, s)
				continue
			}
			prev, ok := byName[s.Name]
			if !ok {
				order = append(order, s.Name)
				byName[s.Name] = s
			} else {
				byName[s.Name] = mergeSpec(prev, s)
			}
		}
	}
	out := make([]ModelSpec, 0, len(order)+len(nameless))
	for _, n := range order {
		out = append(out, byName[n])
	}
	return append(out, nameless...)
}

var specNamePattern = regexp.MustCompile(`^[a-z0-9-]+$`)

// ValidateSpecs checks every merged spec: name grammar, reserved persona
// names, protocol, model id, context window (>= 4096 for all, present
// for custom specs), and the price pair. presetNames names the specs the
// preset layer supplied; every other spec is custom and must carry its own
// context window. Unknown keys are a decode error (LoadTOML), not a
// validation error.
func ValidateSpecs(specs []ModelSpec, presetNames map[string]bool) error {
	for _, s := range specs {
		if err := validateSpec(s, presetNames[s.Name]); err != nil {
			return err
		}
	}
	return nil
}

func validateSpec(s ModelSpec, isPreset bool) error {
	if s.Name == "" {
		return errors.New("model name is required")
	}
	if !specNamePattern.MatchString(s.Name) {
		return fmt.Errorf("model name %q must match [a-z0-9-]", s.Name)
	}
	if reservedModelName(s.Name) {
		return fmt.Errorf("model name %q is reserved (reserved judge/analyst persona names: %s)",
			s.Name, strings.Join(triage.BuiltInPersonaNames(), ", "))
	}
	switch s.Protocol {
	case ProtocolOpenAI, ProtocolAnthropic, ProtocolGemini, ProtocolAzure:
	default:
		return fmt.Errorf("model %q: unknown protocol %q (want openai|anthropic|gemini|azure)", s.Name, s.Protocol)
	}
	// gemini is preset-only: the factory never consults spec.Endpoint for it,
	// so accepting a gemini+endpoint spec would silently target Google's real
	// API instead of the configured server. Reject it at load instead.
	if s.Protocol == ProtocolGemini && s.Endpoint != "" {
		return fmt.Errorf("model %q: protocol gemini is preset-only and does not support a custom endpoint", s.Name)
	}
	if s.Model == "" {
		return fmt.Errorf("model %q: model id is required", s.Name)
	}
	if s.ContextWindow == 0 {
		if isPreset {
			return fmt.Errorf("model %q: context_window must be >= 4096 (presets default to 128000)", s.Name)
		}
		return fmt.Errorf("model %q: context_window is required for custom models (>= 4096)", s.Name)
	}
	if s.ContextWindow < 4096 {
		return fmt.Errorf("model %q: context_window must be >= 4096 (got %d)", s.Name, s.ContextWindow)
	}
	if (s.PriceIn == nil) != (s.PriceOut == nil) {
		return fmt.Errorf("model %q: price_in and price_out must be set together", s.Name)
	}
	return nil
}

// reservedModelName reports whether the name collides with a judge/analyst
// persona. Results are keyed by provider name; a model named like a persona
// would silently collide with its results row. FocusFor's persona map is the
// source of truth, so the check stays in sync with the built-in personas.
func reservedModelName(name string) bool {
	_, ok := triage.FocusFor(name)
	return ok
}
