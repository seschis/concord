package provider

import "testing"

// clearCredentialEnvs blanks every credential environment variable the
// factory or the resolvability predicate consults, so tests are independent
// of the host machine's credentials. t.Setenv restores each at test end.
func clearCredentialEnvs(t *testing.T) {
	t.Helper()
	for _, env := range []string{
		"OPENAI_API_KEY", "AZURE_OPENAI_API_KEY", "GOOGLE_API_KEY",
		"ANTHROPIC_API_KEY", "ANTHROPIC_AUTH_TOKEN", "ANTHROPIC_PROFILE",
		"AWS_REGION", "AWS_DEFAULT_REGION", "HARMONIA_TEST_KEY",
	} {
		t.Setenv(env, "")
	}
}

func TestPresetSpecs(t *testing.T) {
	cases := []struct {
		spec     ModelSpec
		name     string
		protocol Protocol
		model    string
	}{
		{PresetClaude, "claude", ProtocolAnthropic, "claude-opus-5"},
		{PresetGemini, "gemini", ProtocolGemini, "gemini-2.5-flash"},
		{PresetOpenAI, "openai", ProtocolOpenAI, "gpt-5.6-sol"},
		{PresetAzure, "azure", ProtocolAzure, "gpt-5.5"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if c.spec.Name != c.name {
				t.Errorf("Name = %q, want %q", c.spec.Name, c.name)
			}
			if c.spec.Protocol != c.protocol {
				t.Errorf("Protocol = %q, want %q", c.spec.Protocol, c.protocol)
			}
			if c.spec.Model != c.model {
				t.Errorf("Model = %q, want %q", c.spec.Model, c.model)
			}
			if c.spec.ContextWindow != 128000 {
				t.Errorf("ContextWindow = %d, want 128000", c.spec.ContextWindow)
			}
			if c.spec.PriceIn != nil {
				t.Errorf("presets must carry no explicit price, got PriceIn %v", *c.spec.PriceIn)
			}
			if c.spec.PriceOut != nil {
				t.Errorf("presets must carry no explicit price, got PriceOut %v", *c.spec.PriceOut)
			}
		})
	}
	if PresetAzure.APIVersion != "2024-12-01-preview" {
		t.Errorf("PresetAzure.APIVersion = %q, want the current default", PresetAzure.APIVersion)
	}
}

func TestModelSpecResolveKey(t *testing.T) {
	clearCredentialEnvs(t)
	t.Setenv("HARMONIA_TEST_KEY", "from-env")

	// A literal key wins over everything.
	if got := resolveKey(ModelSpec{Protocol: ProtocolOpenAI, APIKey: "literal"}); got != "literal" {
		t.Errorf("literal key = %q, want %q", got, "literal")
	}
	// An env: reference resolves the named variable.
	if got := resolveKey(ModelSpec{Protocol: ProtocolOpenAI, APIKey: "env:HARMONIA_TEST_KEY"}); got != "from-env" {
		t.Errorf("env: reference = %q, want %q", got, "from-env")
	}
	// An unset env: reference falls through to the protocol default.
	t.Setenv("OPENAI_API_KEY", "openai-default")
	if got := resolveKey(ModelSpec{Protocol: ProtocolOpenAI, APIKey: "env:HARMONIA_TEST_UNSET"}); got != "openai-default" {
		t.Errorf("unset env: reference = %q, want the protocol default", got)
	}
	// An empty key resolves the protocol's default credential env var.
	if got := resolveKey(ModelSpec{Protocol: ProtocolOpenAI}); got != "openai-default" {
		t.Errorf("openai default = %q, want %q", got, "openai-default")
	}
	t.Setenv("AZURE_OPENAI_API_KEY", "azure-default")
	if got := resolveKey(ModelSpec{Protocol: ProtocolAzure}); got != "azure-default" {
		t.Errorf("azure default = %q, want %q", got, "azure-default")
	}
	t.Setenv("GOOGLE_API_KEY", "gemini-default")
	if got := resolveKey(ModelSpec{Protocol: ProtocolGemini}); got != "gemini-default" {
		t.Errorf("gemini default = %q, want %q", got, "gemini-default")
	}

	// Anthropic must never lift env credentials into the client option: the
	// SDK's chain reads them lazily, and lifting ANTHROPIC_AUTH_TOKEN would
	// downgrade bearer auth to an x-api-key header.
	t.Setenv("ANTHROPIC_API_KEY", "never-lifted")
	t.Setenv("ANTHROPIC_AUTH_TOKEN", "never-lifted")
	if got := resolveKey(ModelSpec{Protocol: ProtocolAnthropic}); got != "" {
		t.Errorf("anthropic resolveKey = %q, want empty (SDK reads env lazily)", got)
	}
}

func TestModelSpecResolvable(t *testing.T) {
	clearCredentialEnvs(t)

	cases := []struct {
		name       string
		spec       ModelSpec
		env        map[string]string
		wantOK     bool
		wantReason bool
	}{
		{"openai explicit key", ModelSpec{Protocol: ProtocolOpenAI, APIKey: "k"}, nil, true, false},
		{"openai keyless no endpoint", ModelSpec{Protocol: ProtocolOpenAI}, nil, false, true},
		{"openai keyless local endpoint", ModelSpec{Protocol: ProtocolOpenAI, Endpoint: "http://127.0.0.1:8000/v1"}, nil, true, false},
		{"openai env key", ModelSpec{Protocol: ProtocolOpenAI}, map[string]string{"OPENAI_API_KEY": "k"}, true, false},
		{"azure env key", ModelSpec{Protocol: ProtocolAzure}, map[string]string{"AZURE_OPENAI_API_KEY": "k"}, true, false},
		{"azure keyless", ModelSpec{Protocol: ProtocolAzure}, nil, false, true},
		{"gemini env key", ModelSpec{Protocol: ProtocolGemini}, map[string]string{"GOOGLE_API_KEY": "k"}, true, false},
		{"gemini keyless", ModelSpec{Protocol: ProtocolGemini}, nil, false, true},
		{"anthropic keyless no env", ModelSpec{Protocol: ProtocolAnthropic}, nil, false, true},
		{"anthropic auth token", ModelSpec{Protocol: ProtocolAnthropic}, map[string]string{"ANTHROPIC_AUTH_TOKEN": "k"}, true, false},
		{"anthropic explicit key", ModelSpec{Protocol: ProtocolAnthropic, APIKey: "k"}, nil, true, false},
		{"anthropic keyless local endpoint", ModelSpec{Protocol: ProtocolAnthropic, Endpoint: "http://127.0.0.1:9999"}, nil, true, false},
		{"bedrock keyless no region", ModelSpec{Protocol: ProtocolAnthropic, Bedrock: true}, nil, false, true},
		{"bedrock env region", ModelSpec{Protocol: ProtocolAnthropic, Bedrock: true}, map[string]string{"AWS_REGION": "us-east-1"}, true, false},
		{"unknown protocol", ModelSpec{Protocol: "mystery"}, nil, false, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			for k, v := range c.env {
				t.Setenv(k, v)
			}
			ok, reason := c.spec.Resolvable()
			if ok != c.wantOK {
				t.Fatalf("Resolvable() = %v (reason %q), want %v", ok, reason, c.wantOK)
			}
			if c.wantReason && reason == "" {
				t.Error("want a skip reason for an unresolvable spec")
			}
		})
	}
}
