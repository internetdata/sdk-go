#!/bin/bash

# Runs the integration suite against the module as PUBLISHED on the proxy, which
# is the one thing the unit suite cannot check: that suite tests this working
# tree, so it stays green through a tag that was never pushed, a nested module
# that swallowed a file, or an import path a consumer cannot resolve.
#
#   ./scripts/run.sh
#
# Two conditions make the run meaningless rather than failing, and each one skips
# with a reason instead:
#
#   1. Nothing published satisfies the version go.mod requires. Before the first
#      release there is no artifact to test, and unlike an interpreted language a
#      Go test file naming a method that version does not have will not COMPILE,
#      so this gate covers the whole suite rather than one test.
#   2. The staging key is missing. The suite skips from inside itself in that
#      case, so the skip and its reason land in the test output rather than in
#      this script's preamble.

set -euo pipefail

cd "$(dirname "$0")/.."

MODULE="github.com/internetdata/sdk-go"
goModBackup=""

function main() {
    local floor published latest
    floor="$(requiredVersion)"
    published="$(publishedVersions)"
    latest="${published##* }"

    if [ -z "$published" ] || [ "$(newerOf "$floor" "$latest")" != "$latest" ] ; then
        skip "no published ${MODULE} satisfies ${floor}, so there is no released artifact to test"
        return 0
    fi
    echo "==> ${MODULE} ${floor}+ matches published ${published// /, }"

    # go.mod is moved to the newest release for the run and put back afterwards,
    # so a daily run keeps testing whatever is newest instead of pinning the
    # version somebody happened to commit, and a local run leaves no diff behind.
    # The backup path is a global: an EXIT trap runs after main has returned, so
    # a local would be out of scope by the time it fires.
    goModBackup="$(mktemp)"
    cp go.mod "$goModBackup"
    trap restoreGoMod EXIT

    go get "${MODULE}@${latest}"
    go mod tidy
    assertFromTheProxy "$latest"

    go test -count=1 -v ./...
}

# Every tag the module proxy will serve, ascending. This is the resolver `go get`
# itself uses, so the answer is exactly what an install would see. An unpublished
# module answers with an empty list rather than an error, and so does one whose
# repository does not exist, and both mean the same thing here.
#
# Asked from a scratch module rather than this one: while the required version is
# still unpublished, every command that loads THIS module's graph fails, which is
# the state the question is being asked in.
function publishedVersions() {
    local probe versions
    probe="$(mktemp -d)"
    versions="$(cd "$probe" && go mod init sdkprobe >/dev/null 2>&1 &&
        go list -m -versions -f '{{range .Versions}}{{.}} {{end}}' "$MODULE" 2>/dev/null || true)"
    rm -rf "$probe"
    echo "$versions" | tr -s ' ' | sed 's/ $//'
}

function requiredVersion() {
    local line
    line="$(grep -m1 -F "${MODULE} v" go.mod)"
    line="${line%%//*}"
    echo "${line##* }"
}

function newerOf() {
    printf '%s\n%s\n' "$1" "$2" | sort -V | tail -1
}

# The suite is worthless if the toolchain handed it the working tree, and that
# failure is silent: every test passes, against the wrong code. A pseudo-version
# is the same class of mistake, since `@latest` resolves to the tip of the
# default branch when a repository carries no tags at all.
function assertFromTheProxy() {
    local want="$1" version dir replaced
    version="$(go list -m -f '{{.Version}}' "$MODULE")"
    dir="$(go list -m -f '{{.Dir}}' "$MODULE")"
    replaced="$(go list -m -f '{{with .Replace}}{{.Path}}{{end}}' "$MODULE")"

    if [ -n "$replaced" ] ; then
        echo "==> FAILED: ${MODULE} is replaced by ${replaced}, so this would not test the release" >&2
        exit 1
    fi
    if [ "$version" != "$want" ] ; then
        echo "==> FAILED: resolved ${version}, want the published ${want}" >&2
        exit 1
    fi
    case "$dir" in
        "$(go env GOMODCACHE)"/*) ;;
        *)
            echo "==> FAILED: ${MODULE} was built from ${dir}, which is not the module cache" >&2
            exit 1
            ;;
    esac
    echo "==> testing ${MODULE}@${version} from ${dir}"
}

function restoreGoMod() {
    if [ -n "$goModBackup" ] ; then
        mv -f "$goModBackup" go.mod
    fi
}

function skip() {
    echo "==> SKIPPED: $1"
    notice "Integration suite skipped: $1"
}

# Surfaced on the workflow run itself, so a skip is visible without opening the
# log and reading to the end of it.
function notice() {
    if [ "${GITHUB_ACTIONS:-}" = "true" ] ; then
        echo "::notice title=Integration::$1"
    fi
}

main "$@"
