# Validation boundaries

Unit tests verify configuration and lifecycle decisions with controlled inputs.
Linux integration tests use disposable disk images to exercise native libguestfs.
GitHub artifact builds verify distribution compilation and runtime linking.
Neither proves that installed Proxmox modules, cluster locks, or a real VM behave
correctly on your destination host.

## Debian 13 installation

On amd64 Debian 13 / Proxmox 9:

- Download that distribution's binary and verify its published SHA256 checksum.
- Run the installer on a clean disposable host with native packages available.
- Confirm `pvedisk --help`, `pvedisk --version`, and dynamic library resolution.
- Confirm that no Go compiler or Python bindings are needed to execute the binary.
- On Proxmox, run `pvedisk doctor` and resolve every failed prerequisite.

## Disposable Proxmox acceptance

On Proxmox 9, use a guest you can destroy and restore:

- Exercise file-backed, LVM-backed, and ZFS-backed guest disks where available.
- Verify a separate `/boot`, `/home`, guest LVM, and an additional filesystem.
- Confirm media, cloud-init, EFI, TPM, swap, and unattached disks are not exposed
  as ordinary repairable guest filesystems.
- With configuration absent, verify noninteractive mount and
  `pvedisk utils reset-root VMID` fail
  before shutting down the guest. Cancel setup and confirm no configuration or
  VM changes occurred.
- Reject existing locks, pending disk changes, HA-managed guests, suspended
  guests, ambiguous roots, encrypted disks, occupied paths, and missing volumes.
- Start a guest and verify both writable and read-only mount requests refuse it
  with a stop instruction, without changing the VM or its saved session. Stop
  the guest, leave a dead-worker session, and verify mounting safely cleans it
  and continues. Live appliances, disk users, foreign mounts, and uncertain
  ownership must still block recovery.
- For optional offline maintenance, verify graceful shutdown times out without
  forcing the guest off or accessing its disks.
- During mounting, try a normal `qm start`; it must fail due to VM protection.
- Check `ismounted VMID` as root: exit `0` for a complete read-only or writable
  session, silent exit `1` before mounting and after unmounting, and exit `2`
  with a diagnostic for incomplete or conflicting mounts and inspection failures.
  Confirm invalid arguments also exit `2`. Check that no configuration or state
  is created by the probe, and that a saved `mounted` step with no remaining
  views returns `1` without releasing startup protection.
- Mount read-only and verify an attempted edit fails; unmount and verify the
  original guest data is intact.
- Mount writable, make a recognizable edit, unmount, boot, and verify persistence.
- Exit the initiating terminal and verify the detached session remains usable.
- Keep a shell in the repair tree and request unmount; protection must remain on
  a busy-mount failure. Exit the shell and retry.
- Interrupt the worker in each lifecycle phase and verify cleanup reconciles
  ownership, mounts, and appliance disk access before clearing protection.
- Reboot the host with a session present and verify preserved-state cleanup.
- Attempt a destination change with an active and then unresolved session.
- Reset root with matching passwords; boot and verify authentication. Repeat with
  mismatched passwords and confirm no disk modification.
- Verify no password appears in logs, state, process arguments, or persistent
  temporary files. Confirm no operation automatically starts the guest.

Record host versions, binary checksums, results, and any limitations. Treat
unexecuted checks as pending rather than passed.
