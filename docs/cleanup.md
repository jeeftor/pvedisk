# Session cleanup

Sessions record disk paths and identities, public mounts, internal staging, worker identity, and
lifecycle phase beneath `/var/lib/pvedisk`. Treat this as operational state;
preserve it until cleanup succeeds. Configuration changes do not override paths
recorded in a session.

1. Run `pvedisk status VMID` as root. Record its phase and error before taking
   action. Keep the guest stopped.
2. Close shells, editors, and other processes using the public repair directories.
   You can inspect users with `fuser -vm /your/repair-directory/vmVMID`.
3. Run `pvedisk unmount VMID` to request orderly cleanup from a live worker.
4. If the worker has exited, run `pvedisk clean VMID`. Cleanup must reconcile
   actual processes and mounts before releasing an owned Proxmox lock. If it
   cannot prove cleanup or lock ownership, it returns an error and leaves
   protection in place.
5. Start an existing guest only after cleanup succeeds and you verify that its
   configuration still identifies the guest you intend to start. Cleanup prints
   a cleanup confirmation; a deleted VM cannot be started. An unmount prints a
   start command only when the VM is confirmed to exist locally.

Orderly cleanup removes public bind views and the FUSE export, unmounts guest
filesystems, synchronizes writes, shuts down the appliance, and then releases VM
protection. A busy mount is an error, not permission to detach it lazily.

Do not delete state, use `qm unlock`, or pass `--skiplock` to another tool to work
around an error. Root can bypass Proxmox locks; doing so while an appliance has
disk access can corrupt your guest. If cleanup reports changed VM configuration
or another owner's lock, investigate the ownership change before proceeding.

After host reboot, verify that the appliance and its disk access are gone and run
cleanup against the preserved state. A missing configuration file is not a reason
to recreate the mount root or discard session records.

## Automatic recovery on mount

`pvedisk mount VMID` first requires the VM to be stopped; running VMs receive
an error with `qm shutdown VMID` before session cleanup or worker startup.
For the requested VM only, a recorded worker that has exited triggers automatic
cleanup using the same checks as `clean`. Successful cleanup reports progress
and continues mounting. A dead process identifier alone never authorizes removal:
a live appliance, busy or foreign mounts, disk users, unexpected files, or
uncertain lock ownership still block recovery and retain the session.

Worker identity includes process start time and host boot identity, so a reused
process identifier does not keep an old session active. Exited zombie processes
are also treated as dead. During the kernel disk scan, a process whose mountinfo
returns `EINVAL` is skipped only when its mount namespace is confirmed absent;
permission errors and other uncertain inspection failures still block cleanup.

A session with no worker process identifier may still have a worker starting.
It requires explicit cleanup rather than automatic removal. Read-only status,
worker listings, and `ismounted` never clean sessions. Automatic recovery applies
to explicit `mount VMID` requests, including scripts and MCP calls.

## Check mounts in scripts

Run `pvedisk ismounted VMID` as root to test your current pvedisk mount views
without loading site configuration or changing state. It exits silently with
`0` when all recorded views are mounted with matching ownership and a confirmed
live worker, or `1` when there is no recorded session or none of its views remain
mounted. Invalid arguments, inspection failures, partial mounts, conflicts, and
uncertain observations exit `2` with a diagnostic on standard error.

Exit `1` does not prove that cleanup finished, startup protection was released,
or your VM is safe to start. Only recorded pvedisk sessions are checked; mounts
from other tools or mounts whose session record was removed are outside this
check. Use `pvedisk status VMID` to inspect retained state and `pvedisk clean VMID`
to reconcile an interrupted session. Each result is a snapshot.

## Busy mounts and disk users

A failed unmount reports the exact mount path, the `umount` error, and a bounded
`fuser` listing of process identifiers, command names, and access types. In its
ACCESS column, `c` means the process has its current directory inside the mount;
`r` means a root directory, `f`/`F` an open file, `e` an executable, and `m` a
mapped file. Run `cd /` in the affected shell, or close the files through the
identified application, before retrying the printed cleanup command. Changing
directories in a different shell does not release the affected process.

If a disk remains open after the public views are gone, the error identifies its
process identifier and command name, the open `/proc/PID/fd/FD` descriptor, and
its target when available. Use the printed `readlink` command to inspect the
descriptor. A target ending in `(deleted)` is still open despite its pathname
being gone. A disk labeled `(replacement)` belongs to the current disk at a
pathname reused by a rebuilt VM; cleanup checks both the recorded original disk
and its replacement. Close the identified application's disk access, or wait
for a short-lived tool to finish, then retry cleanup. Process arguments are not
printed.

Public mounts can be gone while an unresolved session still retains startup
protection. Directory removal and protection release happen only after all
cleanup checks pass. `pvedisk status VMID` shows the saved session separately
from actual mounts and worker health.

When the recorded worker has exited, `unmount` prints the exact `clean`
command. If a live worker cannot be contacted, inspect the reported socket,
private worker log, and session status. A lost worker response means cleanup was
not confirmed; check status before retrying. Cleanup also reports a still-live
recorded appliance by process identifier and prints a `ps` command that displays
its command name. Protection stays in place while that appliance remains live.

## Deleted VMs and disks

Deleting a VM does not erase its repair-session record. Run `pvedisk clean VMID`
to reconcile its recorded worker, appliance, mounts, and disk users. If the VM
configuration is gone, cleanup releases only a matching owned marker after
checking that the VM is not running. If you rebuilt a stopped VM with the same
identifier and its current configuration has no lock, cleanup can remove the
old matching session marker without changing that configuration. A different
owner's marker or another existing lock still prevents cleanup. When the current
configuration still has the session's create lock, its saved configuration
fingerprint must also match before cleanup can release that lock.

New sessions preserve each disk's filesystem device and inode, block-device
number when applicable, and host boot identity. If a disk path disappears during
the same host boot, cleanup checks that saved identity against process file
descriptors, kernel mounts, active swap, block holders, and loop mappings. This
detects an image still open after deletion, including access through a hard link,
and a block volume opened through another device-node alias. If a rebuilt VM
reuses the disk pathname during the same boot, cleanup checks the original
saved disk identity rather than adopting the replacement. It also checks the
current replacement disk's users before releasing startup protection. Neither
disk is modified. An open or mounted original or replacement disk prevents
cleanup.

Older sessions without a saved disk identity can still be cleaned up when their
original disk paths are accessible. If a path is missing, cleanup retains
protection and asks you to restore access to the original disk or storage before
retrying. The same restriction applies to missing paths after a host reboot or
when the saved boot identity is unavailable: device numbers may have changed.
Reused disk paths require the same boot-bound original identity; uncertainty
about the original disk retains protection even if the replacement is idle.
The error distinguishes a saved identity with no boot identifier from one
recorded before the current host boot. A replacement-path error refers to the
old session's ownership proof; rebuilding the VM does not refresh that proof.
Do not create an empty placeholder image or substitute another volume to pass
this check. These identities are kernel device and inode identities, not
permanent storage identifiers; if storage was remounted or devices were
renumbered, restore access to the original paths and investigate any identity
mismatch. Inaccessible process or kernel ownership information also stops
cleanup rather than assuming a missing path proves the disk is unused.

After successful cleanup, pvedisk removes its recorded empty VM root, extras,
and staging directories, then its session socket, log, and state. Your configured
mount root stays in place. Unexpected files, subdirectories, or replaced paths
stop directory cleanup before protection is released. Cleanup never recursively
deletes directory contents.

## Destination already exists

This error means the exact VM root, extras, or internal staging path already
exists. An empty directory also causes this error. An inspection failure, such
as permission denied, is reported separately. These checks happen before your
worker starts disk access.

A previous tool may have mounted guest partitions under that directory. Never
use `rm -rf` to clear it: deleting through a mounted filesystem deletes guest
files. Rebooting normally removes mounts, but leaves the directories behind.

Use `pvedisk status VMID` to check for a current session and use its normal
unmount/cleanup commands when one exists. For another tool's mounts, inspect
the exact path and its children with `findmnt` and use that tool's cleanup.
Only after confirming that no mounts remain should you move aside old files
or remove empty directories with `rmdir`. Do not remove persistent session state
or an owned lock to bypass this check.
