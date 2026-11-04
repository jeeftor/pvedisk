#!/bin/sh
# Install a native release; configure your mount destination afterward.
set -eu

usage() {
    cat <<'EOF'
Usage: install.sh [--version vX.Y.Z] [--artifact-dir DIRECTORY]

Install the Debian 13/Proxmox 9 amd64 pvedisk binary and runtime packages.
The default download is the latest GitHub release. --artifact-dir installs
from a local release directory containing the binary and SHA256SUMS.
Run pvedisk setup afterward to choose your mount destination.
EOF
}

version=latest
artifact_dir=
while [ "$#" -gt 0 ]; do
    case "$1" in
        --version)
            [ "$#" -ge 2 ] || { usage >&2; exit 2; }
            version=$2
            shift 2
            ;;
        --artifact-dir)
            [ "$#" -ge 2 ] || { usage >&2; exit 2; }
            artifact_dir=$2
            shift 2
            ;;
        -h|--help) usage; exit 0 ;;
        *) usage >&2; exit 2 ;;
    esac
done

if [ "$version" != latest ]; then
    case "$version" in
        v[0-9]*.[0-9]*.[0-9]*) ;;
        *) echo 'Your version must be a release tag such as v0.1.0.' >&2; exit 2 ;;
    esac
    case "$version" in
        *[!a-zA-Z0-9.+-]*) echo 'Your version contains unsupported characters.' >&2; exit 2 ;;
    esac
fi
[ "$(id -u)" -eq 0 ] || { echo 'Run your installer as root.' >&2; exit 1; }
[ "$(uname -s)" = Linux ] || { echo 'You need a Linux host.' >&2; exit 1; }
[ "$(uname -m)" = x86_64 ] || { echo 'You need an amd64 host.' >&2; exit 1; }
[ -r /etc/os-release ] || { echo 'Your host has no /etc/os-release.' >&2; exit 1; }
# shellcheck source=/dev/null
. /etc/os-release
case "${ID:-}:${VERSION_ID:-}" in
    debian:13) ;;
    *) echo 'You need Debian 13/Proxmox 9.' >&2; exit 1 ;;
esac

artifact=pvedisk
workdir=$(mktemp -d)
install_tmp=
cleanup() {
    rm -rf "$workdir"
    if [ -n "$install_tmp" ]; then rm -f "$install_tmp"; fi
}
trap cleanup 0
trap 'exit 1' HUP INT TERM

if [ -n "$artifact_dir" ]; then
    cp "$artifact_dir/$artifact" "$workdir/$artifact"
    cp "$artifact_dir/SHA256SUMS" "$workdir/SHA256SUMS"
else
    if ! command -v curl >/dev/null 2>&1; then
        apt-get update
        DEBIAN_FRONTEND=noninteractive apt-get install -y --no-install-recommends ca-certificates curl
    fi
    if [ "$version" = latest ]; then
        base=https://github.com/jeeftor/pvedisk/releases/latest/download
    else
        base="https://github.com/jeeftor/pvedisk/releases/download/$version"
    fi
    curl --fail --location --proto '=https' --tlsv1.2 "$base/$artifact" -o "$workdir/$artifact"
    curl --fail --location --proto '=https' --tlsv1.2 "$base/SHA256SUMS" -o "$workdir/SHA256SUMS"
fi

awk -v name="$artifact" '$2 == name {print; count++} END {if (count != 1) exit 1}' \
    "$workdir/SHA256SUMS" > "$workdir/selected.sha256" || {
        echo 'Your checksum manifest has no unique entry for this binary.' >&2
        exit 1
    }
(cd "$workdir" && sha256sum --check selected.sha256)

apt-get update
set -- libguestfs0t64 libguestfs-tools libguestfs-xfs fuse3 qemu-system-x86 psmisc libcrypt1
# Your Proxmox kernel is sufficient; avoid installing a second kernel on a PVE host.
kernel_found=false
for kernel in /boot/vmlinuz-*; do
    if [ -f "$kernel" ]; then kernel_found=true; break; fi
done
if [ "$kernel_found" = false ]; then set -- "$@" linux-image-amd64; fi
DEBIAN_FRONTEND=noninteractive apt-get install -y --no-install-recommends "$@"
install -d -m 0755 /usr/local/bin
install_tmp=$(mktemp /usr/local/bin/.pvedisk.XXXXXX)
install -m 0755 "$workdir/$artifact" "$install_tmp"
mv -f "$install_tmp" /usr/local/bin/pvedisk
install_tmp=
echo 'Your pvedisk binary and runtime dependencies are installed.'
echo 'Choose your mount destination by running: pvedisk setup'
