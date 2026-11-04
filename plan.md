# pvedisk: configurable Proxmox disk repair utility

## Project and distribution

- Create the project at ~/devel/pvedisk. That directory does not currently exist, and the current workspace contains no code to move.
- Publish as jeeftor/pvedisk, public, with an MIT license for our code.
- Implement in Go with direct libguestfs bindings, Cobra commands, Viper configuration, and log/slog.
- Release a raw amd64 Linux executable named pvedisk for Proxmox 9/Debian 13 only, with checksums and an installer.
- The installer handles native runtime dependencies and binary installation. Site configuration happens afterward, on the destination host. No Go compiler or Python bindings are required.
- Pin upstream-generated libguestfs bindings and preserve their license notices. Native libraries remain host dependencies; offline dependency bundles are outside version one.

## First-run setup and configuration

Use Bubble Tea, Bubbles text input, and Lip Gloss for a small setup wizard. Cobra provides command routing and Viper loads configuration. Bubble Tea, Cobra, Viper

pvedisk                         # First interactive run opens setup
pvedisk setup                   # Run or revisit the wizard
pvedisk config                  # Display configuration and its location
pvedisk setup --mount-root <directory> --yes

- Require the operator to enter a mount destination. Ship no default destination or site-specific path.
- Ask only for the mount root, validate it, show the resulting VM directory layout, and confirm before saving.
- Require an absolute directory other than /; resolve its canonical location and check access. Ask before creating a missing directory.
- Save mount_root in root-owned /etc/pvedisk/config.yaml, using an atomic write. Support an explicit --config path without searching the working directory.
- After setup, return to the command prompt. Subsequent bare pvedisk invocations show help and the configured destination.
- Without a terminal, never launch the wizard. Print the noninteractive setup command when configuration is missing.
- Mount and password-reset commands require valid configuration; they never initiate setup or stop a VM implicitly because configuration is missing.
- Refuse destination changes while active or unresolved repair sessions exist. Unmount and recovery use recorded session paths even if configuration is missing or damaged.
- Cancelling setup leaves existing configuration and VM state untouched.

## Commands and mount layout

pvedisk doctor
pvedisk mount 400
pvedisk mount 400 --ro
pvedisk status 400
pvedisk unmount 400
pvedisk reset-root 400
pvedisk recover 400

The configured destination determines the layout:

<mount-root>/vm400/                  # Equivalent to the guest’s /
<mount-root>/vm400/etc/shadow
<mount-root>/vm400/boot/
<mount-root>/vm400/home/
<mount-root>/vm400-extra/<filesystem-id>/

- Mount writable by default; --ro explicitly requests read-only access.
- Discover attached disks through Proxmox configuration and storage tools. Support accessible disk files, LVM volumes, and ZFS block volumes.
- Exclude installation media, cloud-init, EFI-variable, TPM, and unattached disks.
- Reconstruct the Linux filesystem hierarchy, including separate mountpoints and LVM. Expose additional mountable filesystems in the sibling extras directory.
- List swap and partition containers without mounting them.
- Use one libguestfs appliance and FUSE session per VM, with internal staging and host bind mounts.
- Fail explicitly for missing disks, encrypted filesystems, ambiguous operating-system detection, or failed mounts. Refuse occupied or conflicting destination paths.

## VM lifecycle and password repair

- Reject existing Proxmox locks, suspended VMs, pending disk changes, and HA-managed VMs.
- Request graceful shutdown for running VMs and wait up to 120 seconds. Abort on timeout without forcing power off.
- Atomically recheck stopped state and acquire lock: create through an embedded helper using installed Proxmox modules. Hold protection throughout disk access.
- Track session ownership, disks, mounts, worker identity, and lifecycle phase in root-only persistent state. A detached worker keeps mounts available after the initiating terminal exits.
- Unmount public views and FUSE, unmount guest filesystems, synchronize, and shut down the appliance before releasing protection.
- Retain protection on busy mounts, failed finalization, or uncertain ownership. Recovery reconciles actual processes and mounts before releasing an owned lock.
- Password reset requires unmounted disks, prompts twice without echo, and supplies the password to virt-customize through a protected temporary file. Never log passwords.
- Never restart automatically. After successful cleanup, print You can now run: qm start 400.

## Verification

- Test first-run setup, cancellation, validation, configuration persistence, noninteractive setup, and destination-change restrictions.
- Test lifecycle failures, conflicting operations, busy mounts, and interrupted-session recovery.
- Run Go formatting, vetting, unit tests, and Linux disk-image integration tests.
- Verify the binary and installation on Debian 13.
- Validate real mounting, persisted edits, read-only enforcement, startup protection, and password recovery on a disposable Proxmox VM.
- Include usage and recovery documentation, CI, and a help-first Makefile. Distinguish local/CI results from actual Proxmox acceptance.
