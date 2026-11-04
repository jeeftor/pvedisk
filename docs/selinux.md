# SELinux inspection and next-boot relabeling

## Read a file's existing label

When you already have access to your guest's mounted filesystem, inspect a
regular file's SELinux label:

```sh
pvedisk utils selinux-context --path /your/repair-directory/vm400/etc/shadow
```

This read-only helper does not require site configuration or a saved pvedisk
session. It reads the selected file without following symbolic links. An absent
or unavailable label is reported distinctly. A stored label describes that
file's metadata; it does not establish whether SELinux is enabled or enforcing
inside your guest. The helper does not change labels or enable SELinux.

## Schedule a relabel for the next guest boot

For a supported Linux guest with SELinux already configured as enforcing or
permissive:

```sh
pvedisk utils selinux-relabel 400
# Or select your existing site configuration explicitly:
pvedisk --config /etc/pvedisk/config.yaml utils selinux-relabel 400
```

Your disks must be unmounted, with no active or unresolved repair session.
Complete `pvedisk unmount 400` or `pvedisk recover 400` first when needed. The
helper requires valid site configuration; it never starts setup for you.

The operation follows the same VM checks and protection as password maintenance:
it rejects conflicting locks and unsafe lifecycle states, gracefully shuts down
a running guest, and holds startup protection during offline disk access. It
checks your guest's SELinux configuration and refuses missing or disabled SELinux.
For multiple attached disks, the helper also refuses order-dependent device names
such as `/dev/sda1` in your guest's `fstab`. `virt-customize` may attach disks in a
different order; use stable filesystem UUIDs or LVM references so maintenance
cannot select another filesystem at the same device name. Password maintenance
uses the same check. This preflight does not rewrite your guest's `fstab`.
The probe also verifies that its mounted root matches the inspected Linux root;
an uncertain or conflicting root identity prevents customization.
Through `virt-customize`, it creates the guest's `/.autorelabel` marker while
disabling immediate relabeling. It never changes your guest's SELinux mode or
starts the VM automatically. Failed or uncertain cleanup retains protection;
use [session recovery](recovery.md) rather than bypassing its lock.

Success means the next-boot marker was scheduled and offline cleanup finished.
It does **not** mean a relabel has already run. When you choose to boot the
guest, its SELinux startup routines perform the relabel, which may take time.
The guest must boot with SELinux enabled for that routine to run. Monitor its
console and verify the guest result after boot. Red Hat documents the marker's
[next-boot behavior](https://docs.redhat.com/en/documentation/red_hat_enterprise_linux/10/html/using_selinux/changing-selinux-states-and-modes).

These optional helpers are CLI utilities; they are not exposed as MCP tools.
