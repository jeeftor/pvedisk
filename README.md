# pvedisk

Repair a stopped Proxmox Linux virtual machine through a reconstructed filesystem
tree. `pvedisk` uses libguestfs directly and keeps a Proxmox startup lock in place
until disk access has finished. Writable mounting is the default; request
`--ro` when you only need to inspect a guest.
Read-only sessions also keep the VM stopped and locked: a running guest could
change the disk underneath your filesystem view. Unmount successfully before
starting the VM. Inspect a separate snapshot or backup copy if the original VM
must remain running.

This project targets amd64 Proxmox 8 (Debian 12) and Proxmox 9 (Debian 13).
Builds require Go 1.27.3, CGO, and the native libguestfs development libraries.
Installed binaries need native runtime packages, but no Go compiler or Python
bindings. GitHub Actions builds each Debian binary in its matching distribution.

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
script, run the explicit noninteractive setup command instead. Mounting and
password reset require valid configuration and never run setup for you.

## Repair a guest

Back up your guest before repairing its disks. The following example uses VM 400:

```console
pvedisk mount
# Enter your VM ID when prompted; or supply it directly:
pvedisk mount 400 --ro
pvedisk status 400
pvedisk unmount 400

pvedisk mount 400
# Edit files beneath <mount-root>/vm400/.
pvedisk unmount 400
qm start 400
```

When you omit the VM ID, `mount` prompts for it in a terminal. Scripts must
provide the ID explicitly. Existing VM destination paths are refused even when
they are empty, so files or mounts from an earlier tool are never hidden.
See [recovery](docs/recovery.md) if your destination already exists.

During startup, `mount` prints progress as the worker changes phases. On success,
it confirms the access mode, guest root, extras directory, discovered filesystems,
and the unmount command. On failure, it gives you the command to inspect the
recorded session. `status` reports when no pvedisk session is recorded; it does
not track mounts created by other tools or list every Proxmox VM.
In a terminal, status uses syntax-colored, indented JSON. Redirected status output
remains plain JSON. Set `NO_COLOR=1` or `TERM=dumb` to disable terminal colors.

`<mount-root>/vm400/` corresponds to the guest's `/`, including separate Linux
mountpoints such as `/boot` and `/home`. Other mountable filesystems appear under
`<mount-root>/vm400-extra/<filesystem-id>/`. Swap and partition containers are
reported without being mounted. Encrypted filesystems, ambiguous Linux roots,
missing disks, and filesystem mount failures abort the operation.

Attached file-backed disks, LVM volumes, and ZFS block volumes are supported when
the host can access them. Installation media, cloud-init, EFI-variable, TPM, and
unattached disks are excluded. Existing locks, suspended or paused guests,
pending changes, and HA management prevent repair. Running guests receive a
graceful shutdown request with a 120-second limit; shutdown is never forced.

A detached worker owns one appliance and one FUSE export for each VM. Your
terminal can exit while you work. Do not bypass the VM lock or start the guest
while a session exists. The utility never restarts your guest automatically.

```console
pvedisk reset-root 400
```

Password reset requires unmounted disks and prompts twice without echo. It passes
the password to `virt-customize` through a protected temporary file and never
includes it in command arguments or logs.

## Offline password helpers

```console
pvedisk utils
pvedisk utils verify-password --shadow /your/repair-directory/vm406/etc/shadow
pvedisk utils hash-password
```

`verify-password` prompts once without echo and compares your known password
against the selected shadow entry. The account defaults to `root`; use
`--user ACCOUNT` to select another entry. It reports a match or mismatch without
printing your password or the stored hash. Empty, locked, or malformed entries
produce a distinct error. Verification does not attempt a login or change files.

`hash-password` prompts twice without echo and prints a salted yescrypt hash alone
on standard output. It does not install that hash or reset a guest password. Both
helpers use native Linux libcrypt and work offline without `mkpasswd` or Python's
`crypt` module. They do not require site configuration or perform VM operations;
you must already have access to the shadow file you select.

## Recover an interrupted session

```console
pvedisk status 400
pvedisk unmount 400
# If the worker was interrupted:
pvedisk recover 400
```

Unmount and recovery use recorded session paths even when configuration is
missing or damaged. Busy mounts and uncertain ownership retain startup protection.
Close processes using the repair tree and retry cleanup. Never use lazy unmount,
`qm unlock`, or remove session state to bypass a failed cleanup. See
[recovery](docs/recovery.md) for the reconciliation procedure.

You cannot change the mount destination while active or unresolved sessions
exist. A successful cleanup prints the command you can run to start your guest.
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

GitHub Actions runs Debian 12/13 builds and disk-image integration tests. Those
checks do not establish actual Proxmox acceptance. Follow the
[acceptance checklist](docs/acceptance.md) on a disposable VM before relying on
the utility for repairs. Our code is MIT licensed; upstream bindings retain their
original notices.
