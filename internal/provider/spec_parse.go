package provider

import (
	"fmt"
	"strconv"
	"strings"
)

// specFlagKeys is the key set a spec accepts, in sort order: the single
// source of truth shared by ModelSpec.Set (the --add-model parser) and the
// unknown-key error text. The keys mirror ModelSpec's field names and
// modelTable's toml tags; TestSpecFlagKeysPinnedToModelSpec pins all three
// together so adding a spec field cannot desync the parser, the error, and
// the TOML table.
var specFlagKeys = []string{"api_key", "api_version", "bedrock", "context_window", "endpoint", "model", "name", "price_in", "price_out", "protocol", "region"}

// Set assigns one already-trimmed key=value pair from a --add-model spec to
// the receiver. Unknown keys are an error naming the registry; type coercion
// errors name the offending key.
func (m *ModelSpec) Set(key, val string) error {
	switch key {
	case "name":
		m.Name = val
	case "protocol":
		m.Protocol = Protocol(val)
	case "endpoint":
		m.Endpoint = val
	case "api_key":
		m.APIKey = val
	case "model":
		m.Model = val
	case "context_window":
		n, err := strconv.Atoi(val)
		if err != nil {
			return fmt.Errorf("context_window must be an integer")
		}
		m.ContextWindow = n
	case "price_in":
		f, err := strconv.ParseFloat(val, 64)
		if err != nil {
			return fmt.Errorf("price_in must be a number")
		}
		m.PriceIn = &f
	case "price_out":
		f, err := strconv.ParseFloat(val, 64)
		if err != nil {
			return fmt.Errorf("price_out must be a number")
		}
		m.PriceOut = &f
	case "bedrock":
		b, err := strconv.ParseBool(val)
		if err != nil {
			return fmt.Errorf("bedrock must be a boolean")
		}
		m.Bedrock = b
	case "region":
		m.Region = val
	case "api_version":
		m.APIVersion = val
	default:
		return fmt.Errorf("unknown key %q (known: %s)", key, strings.Join(specFlagKeys, ", "))
	}
	return nil
}
