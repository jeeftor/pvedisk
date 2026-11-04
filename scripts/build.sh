#!/bin/sh
# Stamp every supported build path with the same version and provenance fields.
set -eu

[ "$#" -le 1 ] || { echo 'Usage: build.sh [OUTPUT]' >&2; exit 2; }
repo_root=$(CDPATH='' cd -- "$(dirname -- "$0")/.." && pwd)
cd "$repo_root"
output=${1:-bin/pvedisk}
base=$(cat VERSION)
if ! printf '%s\n' "$base" | awk '/^(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)$/ {ok=1} END {exit !ok}'; then
    echo 'Your VERSION file must contain a semantic version base such as 0.1.0.' >&2
    exit 1
fi

source=local
tag=
if [ -n "${GITHUB_SHA:-}" ]; then
    commit=$GITHUB_SHA
    source=github
    case "${GITHUB_REF:-}" in refs/tags/*) tag=${GITHUB_REF#refs/tags/} ;; esac
elif [ -n "${CI_COMMIT_SHA:-}" ]; then
    commit=$CI_COMMIT_SHA
    source=gitlab
    tag=${CI_COMMIT_TAG:-}
else
    commit=$(git rev-parse HEAD 2>/dev/null || printf unknown)
fi
if [ "$commit" != unknown ]; then
    case "$commit" in *[!0-9a-fA-F]*) echo 'Your build commit must be a full hexadecimal Git commit.' >&2; exit 1 ;; esac
    [ "${#commit}" -ge 12 ] || { echo 'Your build commit is too short.' >&2; exit 1; }
fi

version=$base
if [ -n "$tag" ] && [ "${tag#v}" != "$version" ]; then
    echo 'Your release tag must match VERSION (an optional v prefix is allowed).' >&2
    exit 1
fi

build_date=$(date -u '+%Y-%m-%dT%H:%M:%SZ')
package=github.com/jeeftor/pvedisk/internal/buildinfo
mkdir -p "$(dirname -- "$output")"
go build -trimpath -buildvcs=false \
    -ldflags "-X $package.Version=$version -X $package.Commit=$commit -X $package.BuildDate=$build_date -X $package.Source=$source" \
    -o "$output" .
