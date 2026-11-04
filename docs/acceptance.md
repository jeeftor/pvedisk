# Validation boundaries

Unit tests verify configuration and lifecycle decisions with controlled inputs.
Linux integration tests use disposable disk images to exercise native libguestfs.
GitHub artifact builds verify distribution compilation and runtime linking.
Neither proves that installed Proxmox modules, cluster locks, or a real VM behave
correctly on your destination host.

## Debian 12 and Debian 13 installation

For each supported amd64 distribution:

- Download that distribution's binary and verify its published SHA256 checksum.
- Run the installer on a clean disposable host with native packages available.
- Confirm `pvedisk --help`, `pvedisk --version`, and dynamic library resolution.
- Confirm that no Go compiler or Python bindings are needed to execute the binary.
- On Proxmox, run `pvedisk doctor` and resolve every failed prerequisite.

## Disposable Proxmox acceptance

Repeat on Proxmox 8 and 9, using a guest you can destroy and restore:

- Exercise file-backed, LVM-backed, and ZFS-backed guest disks where available.
- Verify a separate `/boot`, `/home`, guest LVM, and an additional filesystem.
- Confirm media, cloud-init, EFI, TPM, swap, and unattached disks are not exposed
  as ordinary repairable guest filesystems.
- With configuration absent, verify noninteractive mount and password reset fail
  before shutting down the guest. Cancel setup and confirm no configuration or
  VM changes occurred.
- Reject existing locks, pending disk changes, HA-managed guests, suspended
  guests, ambiguous roots, encrypted disks, occupied paths, and missing volumes.
- Start a guest, mount it, and verify graceful shutdown. Make shutdown time out
  and confirm the utility does not force the guest off or access its disks.
- During mounting, try a normal `qm start`; it must fail due to VM protection.
- Mount read-only and verify an attempted edit fails; unmount and verify the
  original guest data is intact.
- Mount writable, make a recognizable edit, unmount, boot, and verify persistence.
- Exit the initiating terminal and verify the detached session remains usable.
- Keep a shell in the repair tree and request unmount; protection must remain on
  a busy-mount failure. Exit the shell and retry.
- Interrupt the worker in each lifecycle phase and verify recovery reconciles
  ownership, mounts, and appliance disk access before clearing protection.
- Reboot the host with a session present and verify preserved-state recovery.
- Attempt a destination change with an active and then unresolved session.
- Reset root with matching passwords; boot and verify authentication. Repeat with
  mismatched passwords and confirm no disk modification.
- Verify no password appears in logs, state, process arguments, or persistent
  temporary files. Confirm no operation automatically starts the guest.

Record host versions, binary checksums, results, and any limitations. Treat
unexecuted checks as pending rather than passed.
