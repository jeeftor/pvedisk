#!/bin/sh
# Check metadata selection independently of native libguestfs compilation.
set -eu
repo_root=$(CDPATH='' cd -- "$(dirname -- "$0")/.." && pwd)
test_dir=$(mktemp -d)
trap 'rm -rf "$test_dir"' 0
trap 'exit 1' HUP INT TERM
mkdir -p "$test_dir/bin"
cat > "$test_dir/bin/go" <<'EOF'
#!/bin/sh
printf '%s\n' "$@" > "$BUILD_TEST_ARGS"
EOF
cat > "$test_dir/bin/git" <<'EOF'
#!/bin/sh
if [ "${BUILD_TEST_NO_GIT:-}" = true ]; then exit 1; fi
printf '%s\n' "$BUILD_TEST_COMMIT"
EOF
chmod +x "$test_dir/bin/go" "$test_dir/bin/git"
export PATH="$test_dir/bin:$PATH"
export BUILD_TEST_ARGS="$test_dir/arguments"
export BUILD_TEST_COMMIT=0123456789abcdef0123456789abcdef01234567
base=$(cat "$repo_root/VERSION")
package=github.com/jeeftor/pvedisk/internal/buildinfo
unset GITHUB_SHA GITHUB_REF CI_COMMIT_SHA CI_COMMIT_TAG

check() {
    expected_version=$1
    expected_source=$2
    expected_commit=${3:-$BUILD_TEST_COMMIT}
    sh "$repo_root/scripts/build.sh" "$test_dir/pvedisk"
    for field in "Version=$expected_version" "Source=$expected_source" "Commit=$expected_commit"; do
        if ! awk -v field="$package.$field" 'index($0, field) {found=1} END {exit !found}' "$BUILD_TEST_ARGS"; then
            echo "Your build metadata is missing $field." >&2
            exit 1
        fi
    done
    awk '/BuildDate=[0-9][0-9][0-9][0-9]-[0-9][0-9]-[0-9][0-9]T[0-9][0-9]:[0-9][0-9]:[0-9][0-9]Z/ {found=1} END {exit !found}' "$BUILD_TEST_ARGS"
}

# Invoke from outside the repository to exercise path resolution.
cd "$test_dir"
check "$base-dev+0123456789ab" local
export GITHUB_SHA=$BUILD_TEST_COMMIT GITHUB_REF=refs/heads/master
check "$base-dev+0123456789ab" github
export GITHUB_REF=refs/tags/v1.2.3-rc.1+build.2
check v1.2.3-rc.1+build.2 github
unset GITHUB_SHA GITHUB_REF
export CI_COMMIT_SHA=$BUILD_TEST_COMMIT
check "$base-dev+0123456789ab" gitlab
export CI_COMMIT_TAG=v1.2.3
check v1.2.3 gitlab
for invalid in v01.2.3 v1.2.3-01 'v1.2.3 invalid' v1.2; do
    CI_COMMIT_TAG=$invalid
    export CI_COMMIT_TAG
    if sh "$repo_root/scripts/build.sh" "$test_dir/pvedisk" >/dev/null 2>&1; then
        echo "Your build accepted an invalid release tag: $invalid" >&2
        exit 1
    fi
done
unset CI_COMMIT_SHA CI_COMMIT_TAG
export BUILD_TEST_NO_GIT=true
check "$base-dev+unknown" local unknown
echo 'Your build metadata checks passed.'
