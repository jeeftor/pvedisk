# pvedisk

Repair a stopped Proxmox Linux virtual machine through a reconstructed filesystem
tree. `pvedisk` uses libguestfs directly and keeps a Proxmox startup lock in place
until disk access has finished. Writable mounting is the default; request
`--ro` when you only need to inspect a guest.
Read-only sessions also keep the VM stopped and locked: a running guest could
change the disk underneath your filesystem view. Unmount successfully before
starting the VM. Inspect a separate snapshot or backup copy if the original VM
must remain running.

This project targets amd64 Proxmox 9 (Debian 13). The executable is named `pvedisk`.
Builds require Go 1.27.3, CGO, and the native libguestfs development libraries.
Installed binaries need native runtime packages, but no Go compiler or Python
bindings. GitHub Actions and GitLab CI build the binary in Debian 13 containers.

## Identify your build

```console
pvedisk version
pvedisk version --short
pvedisk version --json
```

The version command uses colored text without a surrounding box and shows your
source version, full commit, UTC build timestamp,
build source, Go version, and operating system/architecture. `--short` prints
only the version; `--json` exposes the same metadata for automation. `--version`
also prints the version without requiring configuration or root access.

`make build` and both CI systems use the same build script and display the plain
number in `VERSION`. Every committed change increases that number; patch increases
are the default, with minor or major increases when Semantic Versioning requires
them. Commit IDs remain separate metadata. Release tags must match `VERSION`,
with an optional `v` prefix. An unstamped `go run` or direct `go build` displays
`unversioned` and unknown metadata.

## Set up your host

Download the installer and matching binary/checksum artifacts from a release or
a successful GitHub Actions build. See [installation](docs/installation.md).
Run all host operations as root.

```console
pvedisk doctor
pvedisk setup
# Or choose your own absolute destination explicitly:
pvedisk setup --mount-root /your/repair-directory --yes
pvedisk config
```

There is no default mount destination. Setup asks you for a directory, validates
its canonical path, previews the layout, and asks before creating or saving it.
On Linux, your destination must be owned by root and must not be writable by
your group or other users.
The default configuration file is `/etc/pvedisk/config.yaml`; use `--config` to
select a specific file. Configuration is never loaded from your working directory.
Cancelling setup leaves your configuration and virtual machines untouched.

The first bare `pvedisk` invocation opens setup only when you have a terminal.
After configuration, the bare command displays help and your destination. In a
script, run the explicit noninteractive setup command instead. Mounting,
`utils reset-root`, and `utils selinux-relabel` require valid configuration and
never run setup for you.

## Repair a guest

Back up your guest before repairing its disks. The following example uses VM 400:

```console
pvedisk mount
# Choose a VM from the local host inventory; or supply its ID directly:
pvedisk mount 400 --ro
pvedisk status 400
pvedisk unmount 400

pvedisk mount 400
# Edit files beneath <mount-root>/vm400/.
pvedisk unmount 400
qm start 400
```

Mount requires your VM to be stopped, including with `--ro`. If it is running,
pvedisk refuses the request and prints `qm shutdown VMID`; it does not stop the
VM for you. After confirming the VM is stopped, `mount VMID` automatically
reconciles a saved session whose recorded worker has exited, then continues
mounting with your original options. Recovery checks the appliance, mounts, disk
users, directories, and lock ownership before removing state. It retains
protection and reports the blocker when cleanup cannot be proven safe.

For an active session or a pending worker without a recorded process identity,
an interactive `mount` offers to clean it up and retry mounting. The confirmation defaults to **No**. Cleanup may
unmount your existing repair mounts and must pass the normal ownership and disk
safety checks before mounting is retried once, with your original options.
Other mount errors do not offer a cleanup prompt. Without a terminal, active
or pending sessions return an error; explicitly unmount or clean them before
retrying. Dead-worker recovery also works in scripts and through MCP.

When you omit the VM ID, `mount` lists the local host's VMs, shows their state
and repair availability, and prompts for an eligible ID in a terminal. Scripts
must provide the ID explicitly. Existing VM destination paths are refused even when
they are empty, so files or mounts from an earlier tool are never hidden.
See [cleanup](docs/cleanup.md) if your destination already exists.

During startup, `mount` prints progress as the worker changes phases. On success,
it confirms the access mode, guest root, any extras directory, discovered filesystems,
and the unmount command. On failure, it gives you the command to inspect the
recorded session. `status` reports when no pvedisk session is recorded; it does
not track mounts created by other tools or list every Proxmox VM.
The filesystem table includes each full **HOST PATH**: `/` maps to your guest
root directory, `/boot` and `/boot/efi` map below it, and extra filesystems map
below the extras directory. Unmounted filesystems show no host path. The mount
summary uses compact colored text without a surrounding box.
Bare `pvedisk status` shows your mounted disks and saved sessions awaiting
cleanup. The **DISKS** column reports current mounts; **SAVED STEP** and saved
errors describe earlier work. A saved `mounted` step does not mean disks are
still mounted. Details below the compact table distinguish mounted roots from
saved directories. If the local VM list is available, a missing VM is labeled
as not found on this node: it may have been deleted or moved. If that check
fails, VM availability stays unknown and your saved sessions remain visible.
`pvedisk status VMID` shows syntax-colored, indented JSON containing the
recorded state and current observations, including public/internal mounts and
worker liveness. `pvedisk status --json` returns the full session list as JSON.
Use `pvedisk ps` for a compact list of running mount workers, including their VM
IDs, process IDs, read-only or writable modes, current mount status, and saved
mount paths. Startup and failed workers remain listed while their process
identities are confirmed alive; a listed worker does not by itself mean its
disks are mounted. The **MOUNTS** column reports current mounts. Terminal tables
color your VM IDs, process IDs, paths, access modes, and mount states; writable
mode is yellow and read-only mode is green. `pvedisk ps --json` prints only those
public details as plain JSON. Saved sessions without a running worker are
counted separately from records whose worker could not be checked. Both produce
a reminder to use `pvedisk status`, because disks may still be mounted and need
cleanup after their worker exits. That reminder goes to standard error with
`--json`. Neither form loads site
configuration or queries Proxmox VM inventory.
Missing workers, partial mounts, conflicting ownership, and damaged records are
shown explicitly. Only pvedisk sessions are tracked; mounts from other tools are
not adopted. Redirected JSON remains plain. Set `NO_COLOR=1` or `TERM=dumb` to
disable terminal colors.

For scripts, run `pvedisk ismounted VMID` as root. It checks actual mounts and
worker identity without loading site configuration or changing your session.

| Exit code | Meaning |
| --- | --- |
| `0` | All recorded mount views are present and owned by your session, with its worker confirmed alive. |
| `1` | No recorded pvedisk session, or none of its recorded mount views remain mounted. |
| `2` | Invalid arguments, failed inspection, partial mounts, or conflicting or uncertain state. |

Codes `0` and `1` produce no output. Code `2` writes a diagnostic to standard
error. Both writable and read-only mounts count as mounted. Only pvedisk
sessions are tracked; mounts from other tools or mounts whose session record
was removed are outside this check. A result is a snapshot, and exit `1` does
not mean cleanup is complete or your VM is safe to start. Use `status` and
`clean` to resolve retained sessions and startup protection.

```sh
if pvedisk ismounted 400; then
    echo "Your repair session is mounted."
else
    result=$?
    case "$result" in
        1) echo "Your repair session is not mounted." ;;
        *) exit "$result" ;;
    esac
fi
```

`<mount-root>/vm400/` corresponds to the guest's `/`, including separate Linux
mountpoints such as `/boot` and `/home`. Other mountable filesystems appear under
`<mount-root>/vm400-extra/<filesystem-id>/`. Swap and partition containers are
reported without being mounted. Encrypted filesystems, ambiguous Linux roots,
missing disks, and filesystem mount failures abort the operation.

Attached file-backed disks, LVM volumes, and ZFS block volumes are supported when
the host can access them. Installation media, cloud-init, EFI-variable, TPM, and
unattached disks are excluded. Existing locks, suspended or paused guests,
pending changes, and HA management prevent repair. Mount refuses running guests
and atomically rechecks stopped state when acquiring startup protection.

A detached worker owns one appliance and one FUSE export for each VM. Your
terminal can exit while you work. Do not bypass the VM lock or start the guest
while a session exists. The utility never restarts your guest automatically.
The initiating mount command exits after startup; the worker and appliance remain
resident until successful cleanup. Bare `pvedisk unmount` lists those sessions
and prompts for the one you want to clean up. `pvedisk unmount VMID` remains
available for scripts. Sessions whose workers have exited require `clean VMID`.

## Optional password helpers

```console
pvedisk utils
pvedisk utils reset-root 400
pvedisk utils verify-password --shadow /your/repair-directory/vm406/etc/shadow
pvedisk utils hash-password
```

`utils reset-root` resets root passwords on supported Linux guests through
[`virt-customize`](https://libguestfs.org/virt-customize.1.html); it is not limited
to RHEL 10. It requires valid site configuration and unmounted disks, and prompts
twice without echo. It passes the password to `virt-customize` through a
protected temporary file and never includes it in command arguments or logs.
This optional reset helper retains its graceful shutdown request with a
120-second limit and no forced power-off, followed by startup protection and
cleanup. Mount itself requires you to stop the guest first.

`verify-password` prompts once without echo and compares your known password
against the selected shadow entry. The account defaults to `root`; use
`--user ACCOUNT` to select another entry. It reports a match or mismatch without
printing your password or the stored hash. Empty, locked, or malformed entries
produce a distinct error. Verification does not attempt a login or change files.

For automation, redirect or pipe one password into either offline helper:

```sh
pvedisk utils verify-password --shadow /your/repair-directory/vm406/etc/shadow < "$HOME/.rootpw"
pvedisk utils hash-password < "$HOME/.rootpw"
```

Nonterminal input is read without a prompt or confirmation. One trailing newline
or Windows line ending is removed; other password characters, including spaces
and percent signs, are preserved. Empty input, multiple lines, null bytes, and
input exceeding 64 KiB are rejected without printing the supplied password.

`hash-password` prompts twice without echo and prints a salted yescrypt hash alone
on standard output. It does not install that hash or reset a guest password.
`verify-password` and `hash-password` use native Linux libcrypt and work offline
without `mkpasswd` or Python's
`crypt` module. They do not require site configuration or perform VM operations;
you must already have access to the shadow file you select.

## Optional SELinux helpers

```console
pvedisk utils selinux-context --path /your/repair-directory/vm400/etc/shadow
pvedisk utils selinux-relabel 400
```

The context helper reads an existing file label; a label alone does not tell you
whether your guest enforces SELinux. The relabel helper uses protected offline
maintenance to schedule `/.autorelabel` for the next guest boot. It requires
unmounted disks and SELinux already configured as enforcing or permissive; it
does not enable SELinux, relabel immediately, or start the guest. That next boot's
relabel may take time. See [SELinux inspection and scheduling](docs/selinux.md).

## HTTP MCP access

Expose your host's VM inspection and mount tools to an HTTP MCP client:

```sh
pvedisk mcp setup
pvedisk mcp
# Save only server settings, without registering clients:
pvedisk mcp setup --client none
# Explicit tokenless access on your isolated LAN:
pvedisk mcp --listen 0.0.0.0:8766 --no-token
# Or require your private bearer token:
pvedisk mcp --listen 0.0.0.0:8766 --token-file /etc/pvedisk/mcp.token
```

Setup saves MCP settings and registers `pvedisk` in supported clients detected
on your current `PATH`. It configures the current account's clients (root's
clients when run as root), installs no client software, and never starts the
server. `--detect` and `--guide` provide read-only output. Use `--client` to select
all detected clients, none, Claude, Codex, or OpenCode; `--install=false` saves
only server settings. Changed client files receive private backups.

MCP settings default to `$XDG_CONFIG_HOME/pvedisk/mcp.yaml`, with
`~/.config/pvedisk/mcp.yaml` as the fallback. `mcp --mcp-config PATH` selects a
different settings file, independently of the site's `--config`. Server startup
loads saved settings and applies explicit flag overrides without writing files.
The fresh default endpoint is `http://127.0.0.1:8766/mcp`. The server exposes VM and
session inspection, worker listing, version, doctor, mount, unmount, and cleanup.
Mounting remains writable by default; a read-only request still stops and locks
your VM. A disconnected client can leave its detached worker running, so inspect
status before retrying. Password helpers are not exposed over MCP. See
[HTTP MCP setup](docs/mcp.md) for authentication, client connection, tool inputs,
and the optional root systemd service.

## Clean up an interrupted session

```console
pvedisk status 400
pvedisk unmount 400
# If the worker was interrupted:
pvedisk clean 400
```

Unmount and cleanup use recorded session paths even when configuration is
missing or damaged. Busy mounts and uncertain ownership retain startup protection.
Close processes using the repair tree and retry cleanup. Never use lazy unmount,
`qm unlock`, or remove session state to bypass a failed cleanup. See
[cleanup](docs/cleanup.md) for the reconciliation procedure.

You cannot change the mount destination while active or unresolved sessions
exist. `clean` confirms completion without suggesting that you start a possibly
deleted or moved VM. Unmount prints a start command only when your VM is still
listed on this node.
Successful cleanup removes the directories created for that session, its socket,
log, and state file. Your configured mount root remains. Unexpected files or
replaced directories stop cleanup before the startup lock is released.

## Develop and verify

```console
make
make test
make vet
make build
```

Plain `make` prints help. Native Linux builds enable the libguestfs backend through
CGO; other platforms provide a CLI/configuration development build that refuses
disk access. Generated upstream Go bindings are pinned and licensed separately
under `third_party/guestfs`.

GitHub Actions runs Debian 13 builds and disk-image integration tests. Those
checks do not establish actual Proxmox acceptance. Follow the
[acceptance checklist](docs/acceptance.md) on a disposable VM before relying on
the utility for repairs. Our code is MIT licensed; upstream bindings retain their
original notices.
