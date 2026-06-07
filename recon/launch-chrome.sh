#!/usr/bin/env bash
# Launch a clean Chrome instance with remote debugging enabled so the
# capture script can attach. Uses a throwaway profile so it won't touch
# your real Chrome data.
set -euo pipefail

PORT="${1:-9222}"
PROFILE="/tmp/cambly-recon-profile"
CHROME="/Applications/Google Chrome.app/Contents/MacOS/Google Chrome"

echo "Launching Chrome on debug port $PORT with profile $PROFILE"
exec "$CHROME" \
  --remote-debugging-port="$PORT" \
  --user-data-dir="$PROFILE" \
  --no-first-run \
  --no-default-browser-check \
  "https://www.cambly.com/en/student/login"
