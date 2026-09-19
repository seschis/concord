package provider

import anthropicsdk "github.com/anthropics/anthropic-sdk-go"

// cacheTTL is the prompt-cache breakpoint lifetime the Claude adapter requests.
// 5m covers a single finding's back-to-back tool-loop turns while keeping the
// cheaper 1.25x write premium; below the model's minimum cacheable prefix the
// API silently no-ops, so this is safe on every model. If this changes to 1h,
// bump cacheWriteMult in pricing.go to 2.0 (TestCacheWriteMultMatchesTTL guards
// the pairing).
const cacheTTL = anthropicsdk.CacheControlEphemeralTTLTTL5m
