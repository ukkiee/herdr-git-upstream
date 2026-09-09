#!/bin/sh
set -eu
cd "$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)"
exec go build -trimpath -ldflags "-s -w" -o bin/herdr-pull-status ./cmd/herdr-pull-status
