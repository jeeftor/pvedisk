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

## From a GitLab pipeline build

Open your `jeef/pvedisk` pipeline and download the artifacts from
`build-debian12` for Proxmox 8 or `build-debian13` for Proxmox 9. Each artifact
contains a `dist/debian12` or `dist/debian13` directory with the raw binary,
installer, checksum manifest, and licenses. After extracting and copying your
matching directory to the destination host, run as root:

```sh
cd dist/debian12  # Use dist/debian13 for Proxmox 9.
sha256sum --check SHA256SUMS
sh install.sh --artifact-dir "$PWD"
pvedisk --version
pvedisk doctor
pvedisk setup
```

The GitLab pipeline builds with CGO and pinned Go 1.27.3 in public Debian 12 and
13 images. Native builds run formatting, vetting, unit tests, race detection,
and shell checks. Separate fresh Debian jobs verify installation and execution
without a Go compiler. Separate required integration jobs verify disk-image
mounting, persisted edits, and read-only enforcement. Their Docker runner needs
`CAP_SYS_ADMIN` and a usable `/dev/fuse` device, usually supplied through the
runner's privileged-mode configuration. A missing capability fails the
integration job explicitly; the native build artifacts remain downloadable.
These disk-image checks do not establish disposable-Proxmox acceptance.

The pipeline selects runners tagged `mitre-no-cui`. Go downloads verify the
pinned upstream SHA256 checksum. When your runner supplies
`CI_SERVER_TLS_CA_FILE`, the build job combines that chain with Debian's public
certificate roots for its own download and dependency processes. It does not
disable TLS verification or change your workstation's trust settings.

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
GitHub Debian build and integration environment, or `.gitlab-ci.yml` and
`scripts/gitlab-build.sh` for the equivalent GitLab jobs.
