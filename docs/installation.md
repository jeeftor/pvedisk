# Install on your Proxmox host

You need an amd64 Proxmox 9/Debian 13 host with access to
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
install the binary:

```sh
cd pvedisk-artifacts
sha256sum --check SHA256SUMS
sh install.sh --artifact-dir "$PWD"
pvedisk --version
pvedisk doctor
pvedisk setup
```

The bundle includes the raw `pvedisk` executable, the installer, checksums, and
license notices. The binary requires Debian 13 native libraries and C runtime.

## From a GitLab pipeline build

Open your `jeef/pvedisk` pipeline and download the artifacts from
`build-debian13` for Proxmox 9. The artifact contains a `dist` directory with the
raw `pvedisk` binary, installer, checksum manifest, and licenses. After extracting
and copying that directory to the destination host, run as root:

```sh
cd dist
sha256sum --check SHA256SUMS
sh install.sh --artifact-dir "$PWD"
pvedisk --version
pvedisk doctor
pvedisk setup
```

The GitLab pipeline builds with CGO and pinned Go 1.27.3 in a public Debian 13
image. The native build runs formatting, vetting, unit tests, race detection,
and shell checks. A separate fresh Debian 13 job verifies installation and
execution without a Go compiler. A separate required integration job verifies disk-image
mounting, persisted edits, and read-only enforcement. Their Docker runner needs
`CAP_SYS_ADMIN` and a usable `/dev/fuse` device, usually supplied through the
runner's privileged-mode configuration. A missing capability fails the
integration job explicitly; the native build artifacts remain downloadable.
These disk-image checks do not establish disposable-Proxmox acceptance.

The pipeline selects runners tagged `mitre-no-cui`. Go downloads verify the
pinned upstream SHA256 checksum. If your network requires an additional trusted
certificate chain, set the project's `PVEDISK_CA_BUNDLE` CI variable to type
**File**, with the trusted PEM certificates as its value. The build job combines
that file, the runner's readable `CI_SERVER_TLS_CA_FILE`, and existing readable
`SSL_CERT_FILE`, `CURL_CA_BUNDLE`, and `GIT_SSL_CAINFO` files with Debian's public
certificate roots. The combined bundle applies only to that job's downloads and
dependency processes and is removed afterward. The job keeps TLS verification
enabled and does not change your workstation's trust settings.

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

On Debian 13, install a C compiler, `pkg-config`, `libguestfs-dev`, `libcrypt-dev`,
and the libguestfs/FUSE runtime. With Go 1.27.3 available:

```sh
CGO_ENABLED=1 go build -trimpath -o pvedisk .
```

`libguestfs.org/guestfs` resolves to the pinned upstream-generated bindings in
`third_party/guestfs`. Native libraries remain dynamically linked host
dependencies. See `.github/workflows/build.yml` for the complete repeatable
GitHub Debian build and integration environment, or `.gitlab-ci.yml` and
`scripts/gitlab-build.sh` for the equivalent GitLab jobs.
