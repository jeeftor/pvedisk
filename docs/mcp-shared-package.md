# Shared MCP setup package: follow-up design

QMP and pvedisk already use the official Go MCP SDK. Keep that SDK responsible
for protocol handling. The useful shared package would provide the operator
setup surrounding it, compiled into each application. It would not introduce
a third resident process or make either application depend on the other.

## First extraction

Start with their common setup responsibilities, adopting pvedisk's stricter
file and credential checks as the shared contract:

- Detect installed Claude Code, Codex, and supported OpenCode versions.
- Merge one named HTTP server into the correct client configuration format.
- Preserve unrelated entries, JSONC comments, and number precision.
- Refuse malformed files and symbolic-link targets; write private backups and
  replace files atomically with synchronization.
- Generate private bearer-token files only when requested and reuse existing
  credentials when setup runs again.
- Produce setup results and connection guidance without printing credentials.

Pass application identity and endpoint information into these functions. The
entry name is `qmcp` for QMP and `pvedisk` here; neither name belongs as a
constant inside a shared implementation. Client registration must retain the
other application's entry.

Keep client discovery separate from registration. Detection and guide output
must remain read-only. Registration should report per-client results so a
failure cannot look like every client was configured successfully.

TOML rewriting currently preserves values but can normalize formatting and
comments. Preserve the original private backup and document that behavior until
a comment-preserving editor is adopted and verified.

## Application-owned behavior

Keep each application's Cobra commands, server settings schema, default port,
configuration location, and validation policy in that application. Reuse file
and credential primitives without inventing a universal settings schema.

Pvedisk's mount configuration remains separate from its MCP settings. Its root
ownership requirements and VM protections do not belong in a generic setup
package. QMP's inventory, OCR, scripts, and console operations also stay in QMP.

Server setup and client setup can run under different accounts and on different
machines. A common package must take its paths and identity explicitly rather
than assuming root's home directory is the agent user's home directory.

## HTTP lifecycle after setup parity

Authentication middleware, origin validation, bound-address checks, and bounded
HTTP shutdown are further candidates once both applications share conformance
tests. Keep application cleanup outside the transport package.

In particular, pvedisk workers and FUSE appliances must survive an HTTP server
restart. A shared shutdown helper must not unmount disks, release VM protection,
or kill detached children. Pvedisk's systemd unit deliberately uses
`KillMode=process` and the host mount namespace. Do not generate one generic
service sandbox for both applications.

## Migration sequence

1. Establish setup parity in pvedisk and record the common behavior as tests.
2. Choose a module name and hosting location that both build pipelines can
   access. Move the proven client/file/credential helpers and their tests.
3. Adopt a pinned module version in pvedisk, then migrate QMP in a separate
   change. Keep the applications' existing commands and saved configuration
   formats working.
4. Verify both applications against the same client fixtures and HTTP lifecycle
   cases before sharing more transport code.

Consume the new shared module at a pinned version, without development-directory
`replace` directives or floating branches. This does not change pvedisk's
vendored libguestfs binding. Air-gap users continue to receive standalone
application binaries; they do not install the shared module separately.

The follow-up is complete only when both applications consume the package.
Creating a third repository without migrating a consumer would leave the
duplication in place.
