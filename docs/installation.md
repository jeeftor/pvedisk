# Install on your Proxmox host

You need an amd64 Proxmox 8/Debian 12 or Proxmox 9/Debian 13 host with access to
its Debian package repositories. Offline runtime dependency bundles are outside
version one. Site configuration happens after installation.

## From a GitHub Actions build

On a machine with GitHub CLI access, choose a successful run for the implementation
branch and download its combined artifact:

```sh
gh run list --repo jeeftor/pvedisk --branch feature/initial-implementation
gh run download RUN_ID --repo jeeftor/pvedisk \
  --name pvedisk-linux-amd64 --dir pvedisk-artifacts
```

Copy that directory to your destination host. As root, verify the manifest and
install the binary for your host's Debian version:

```sh
cd pvedisk-artifacts
sha256sum --check SHA256SUMS
sh install.sh --artifact-dir "$PWD"
pvedisk --version
pvedisk doctor
pvedisk setup
```

The bundle includes raw `pvedisk-linux-amd64-debian12` and
`pvedisk-linux-amd64-debian13` executables, the installer, checksums, and license
notices. Do not interchange the two distribution binaries: their native library
and C runtime requirements come from the matching build environment.

## From a tagged release

When a tagged release is available, download and review its installer. Then run:

```sh
sh install.sh --version vX.Y.Z
```

Replace `vX.Y.Z` with the actual release tag. With no version argument, the
installer selects the latest release. It verifies the selected binary against the
downloaded checksum manifest, installs native runtime dependencies, and atomically
replaces `/usr/local/bin/pvedisk`. Checksums detect corruption; obtain the
installer and manifest from a trusted source.

The installer does not choose or create a mount destination and does not write
your configuration. Run `pvedisk setup` afterward. The utility requires root for
repair operations and writes private session records beneath `/var/lib/pvedisk`.

## Build yourself

On the matching Debian release, install a C compiler, `pkg-config`,
`libguestfs-dev`, and the libguestfs/FUSE runtime. With Go 1.27.3 available:

```sh
CGO_ENABLED=1 go build -trimpath -o pvedisk .
```

`libguestfs.org/guestfs` resolves to the pinned upstream-generated bindings in
`third_party/guestfs`. Native libraries remain dynamically linked host
dependencies. See `.github/workflows/build.yml` for the complete repeatable
Debian build and integration environment.
