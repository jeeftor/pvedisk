# Session cleanup and recovery

Sessions record disk paths, public mounts, internal staging, worker identity, and
lifecycle phase beneath `/var/lib/pvedisk`. Treat this as operational state;
preserve it until cleanup succeeds. Configuration changes do not override paths
recorded in a session.

1. Run `pvedisk status VMID` as root. Record its phase and error before taking
   action. Keep the guest stopped.
2. Close shells, editors, and other processes using the public repair directories.
   You can inspect users with `fuser -vm /your/repair-directory/vmVMID`.
3. Run `pvedisk unmount VMID` to request orderly cleanup from a live worker.
4. If the worker has exited, run `pvedisk recover VMID`. Recovery must reconcile
   actual processes and mounts before releasing an owned Proxmox lock. If it
   cannot prove cleanup or lock ownership, it returns an error and leaves
   protection in place.
5. Start the guest only after cleanup succeeds and the utility prints
   `You can now run: qm start VMID.`

Orderly cleanup removes public bind views and the FUSE export, unmounts guest
filesystems, synchronizes writes, shuts down the appliance, and then releases VM
protection. A busy mount is an error, not permission to detach it lazily.

Do not delete state, use `qm unlock`, or pass `--skiplock` to another tool to work
around an error. Root can bypass Proxmox locks; doing so while an appliance has
disk access can corrupt your guest. If recovery reports changed VM configuration
or another owner's lock, investigate the ownership change before proceeding.

After host reboot, verify that the appliance and its disk access are gone and run
recovery against the preserved state. A missing configuration file is not a reason
to recreate the mount root or discard session records.
