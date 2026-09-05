#!/bin/bash

# Regenerates internal/api from the pinned spec.
#
# Unlike an ecosystem with a build step, a Go module is consumed as source, so
# the generated file is COMMITTED. Run this after scripts/download-spec.sh and
# commit the spec change and the regenerated client together.
#
# The generator is pinned here rather than in go.mod: a `tool` directive would
# put the whole codegen dependency tree into the module graph of every consumer.

set -euo pipefail

cd "$(dirname "$0")/.."

GENERATOR="${GENERATOR:-github.com/oapi-codegen/oapi-codegen/v2/cmd/oapi-codegen@v2.8.0}"

go run "$GENERATOR" -config oapi-codegen.yaml spec/openapi.yaml
gofmt -l internal/api
echo "internal/api/api_gen.go <- spec/openapi.yaml (${GENERATOR##*@})"
