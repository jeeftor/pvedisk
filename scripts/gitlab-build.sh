#!/bin/sh
# Repeatable Debian CGO builds and separate runner-capability checks for GitLab.
set -eu

prepare_toolchain() {
    tool_dir=$(mktemp -d)
    trap 'rm -rf "$tool_dir"' 0
    trap 'exit 1' HUP INT TERM
    apt-get update
    DEBIAN_FRONTEND=noninteractive apt-get install -y --no-install-recommends \
        ca-certificates curl git build-essential pkg-config libguestfs-dev libcrypt-dev shellcheck

    # Preserve your existing trust inputs while adding job-provided CA chains.
    ca_bundle="$tool_dir/ci-ca.pem"
    cat /etc/ssl/certs/ca-certificates.crt > "$ca_bundle"
    custom_ca=false
    for ca_source in "${SSL_CERT_FILE:-}" "${CURL_CA_BUNDLE:-}" "${GIT_SSL_CAINFO:-}" \
        "${CI_SERVER_TLS_CA_FILE:-}" "${PVEDISK_CA_BUNDLE:-}"; do
        if [ -n "$ca_source" ] && [ -f "$ca_source" ] && [ -r "$ca_source" ]; then
            cat "$ca_source" >> "$ca_bundle"
            printf '\n' >> "$ca_bundle"
            custom_ca=true
        fi
    done
    if [ "$custom_ca" = true ]; then
        export SSL_CERT_FILE="$ca_bundle" CURL_CA_BUNDLE="$ca_bundle" GIT_SSL_CAINFO="$ca_bundle"
    fi

    curl --fail --location --proto '=https' --tlsv1.2 \
        https://go.dev/dl/go1.27.3.linux-amd64.tar.gz -o "$tool_dir/go.tar.gz"
    printf '%s  %s\n' \
        65efbc80fdc1fa2cd2631cad5b481777a68e8725f34e89f50fc6ac714e379b40 \
        "$tool_dir/go.tar.gz" | sha256sum --check -
    tar -xzf "$tool_dir/go.tar.gz" -C "$tool_dir"
    export PATH="$tool_dir/go/bin:$PATH"
    go version
}

build() {
    prepare_toolchain
    test -z "$(gofmt -l main.go internal)" || { gofmt -l main.go internal; exit 1; }
    go mod verify
    shellcheck scripts/*.sh
    make
    go vet ./...
    go test -race -timeout 10m ./...

    version="0.1.0-dev+${CI_COMMIT_SHORT_SHA}"
    if [ -n "${CI_COMMIT_TAG:-}" ]; then version=$CI_COMMIT_TAG; fi
    output="dist/debian${DEBIAN_VERSION}"
    artifact="pvedisk-linux-amd64-debian${DEBIAN_VERSION}"
    mkdir -p "$output"
    go build -trimpath -buildvcs=false \
        -ldflags "-X github.com/jeeftor/pvedisk/internal/cli.Version=$version" \
        -o "$output/$artifact" .
    ldd "$output/$artifact"
    "$output/$artifact" --help
    "$output/$artifact" --version
    cp scripts/install.sh "$output/install.sh"
    cp LICENSE "$output/LICENSE"
    cp third_party/guestfs/COPYING.LIB "$output/COPYING.LIB"
    (cd "$output" && sha256sum "$artifact" install.sh LICENSE COPYING.LIB > SHA256SUMS)
    (cd "$output" && sha256sum --check SHA256SUMS)
}

integration() {
    prepare_toolchain
    DEBIAN_FRONTEND=noninteractive apt-get install -y --no-install-recommends \
        libguestfs-tools libguestfs-xfs fuse3 qemu-system-x86 linux-image-amd64 libcap2-bin
    if ! capsh --has-p=cap_sys_admin; then
        echo 'Your integration runner needs CAP_SYS_ADMIN and access to /dev/fuse. Configure the Docker runner for FUSE (for example, privileged mode). Native build artifacts are available independently.' >&2
        exit 1
    fi
    if [ ! -e /dev/fuse ]; then
        mknod /dev/fuse c 10 229 || {
            echo 'Your integration runner cannot create /dev/fuse. Pass the FUSE device through from the runner host.' >&2
            exit 1
        }
    fi
    if [ ! -c /dev/fuse ] || [ ! -r /dev/fuse ] || [ ! -w /dev/fuse ]; then
        echo 'Your integration runner needs a readable and writable /dev/fuse character device.' >&2
        exit 1
    fi
    go test -v -tags integration -timeout 15m ./internal/appliance
}

installation() {
    output="dist/debian${DEBIAN_VERSION}"
    (cd "$output" && sha256sum --check SHA256SUMS)
    sh "$output/install.sh" --artifact-dir "$PWD/$output"
    /usr/local/bin/pvedisk --help
    /usr/local/bin/pvedisk --version
    if command -v go || command -v gcc; then
        echo 'Your clean runtime verification container unexpectedly contains a compiler.' >&2
        exit 1
    fi
    test ! -e /etc/pvedisk/config.yaml
}

case "${DEBIAN_VERSION:-}" in
    12|13) ;;
    *) echo 'Set DEBIAN_VERSION to 12 or 13 for your matching Debian container.' >&2; exit 2 ;;
esac
case "${1:-}" in
    build) build ;;
    integration) integration ;;
    installation) installation ;;
    *) echo 'Usage: gitlab-build.sh build|integration|installation' >&2; exit 2 ;;
esac
