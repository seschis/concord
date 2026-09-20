#!/bin/sh
# Run the built-in concord demo (two findings against examples/sample-app)
# and record the live TUI with asciinema.
#
# Requires:
#   - Go 1.26+
#   - asciinema 3.x (brew install asciinema)
#   - at least one LLM credential: ANTHROPIC_API_KEY, GOOGLE_API_KEY,
#     OPENAI_API_KEY, or AZURE_OPENAI_API_KEY + AZURE_OPENAI_ENDPOINT
#
# Cost stays low with --effort low. Extra arguments are passed straight to
# concord, e.g.  ./examples/run-demo.sh --no-azure --no-gemini
set -eu
cd "$(dirname "$0")/.."

go build -o bin/concord ./cmd/concord

asciinema record -f asciicast-v2 --overwrite --window-size 110x32 --idle-time-limit 2 \
  examples/demo.cast \
  --command "bin/concord --analyst strict --analyst business --srcroot examples/sample-app -o ./demo-out --effort low examples/findings.sarif $*"

echo
echo "Recording:   examples/demo.cast"
echo "Run output:  demo-out/  (report.md, results.json, transcripts/)"
