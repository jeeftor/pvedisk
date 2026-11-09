# HTTP MCP server

Run `pvedisk mcp` on your Proxmox host to expose inspection and mount operations
to an HTTP MCP client. Run host operations as root. The endpoint is `/mcp` and
the fresh default listener is `127.0.0.1:8766`.

```sh
pvedisk mcp
```

Starting the server loads saved MCP settings, then applies explicitly supplied
flags. It never writes settings, runs setup, loads your site configuration, or
runs doctor. The `mount` tool requires valid site configuration when called.
Select a fixed site configuration file with the global flag:

```sh
pvedisk --config /etc/pvedisk/config.yaml mcp
```

Read-only tools, unmount, and cleanup continue to work without valid site
configuration because they use your host inventory or saved session paths.

## Save settings and configure clients

```sh
pvedisk mcp setup
pvedisk mcp
```

Setup saves your MCP listener/authentication choices and registers the `pvedisk`
entry in supported clients found on your current `PATH`: Claude, Codex, and
OpenCode. It does not install client software or start the server. Client files
belong to your current account; running setup as root configures root's clients,
not another user's or a remote machine's clients.

MCP settings default to `$XDG_CONFIG_HOME/pvedisk/mcp.yaml`, or
`~/.config/pvedisk/mcp.yaml` when `XDG_CONFIG_HOME` is unset. This file is separate
from the site's mount-root configuration selected by `--config`. Tokens stay in
separate private files; MCP YAML stores only their paths. Select an explicit MCP
settings file with `--mcp-config`, supported by both setup and server startup:

```sh
pvedisk mcp setup --mcp-config /etc/pvedisk/mcp.yaml --client none
pvedisk mcp --mcp-config /etc/pvedisk/mcp.yaml
```

Use these controls to choose what setup changes:

```sh
pvedisk mcp setup --detect                 # List installed clients; no settings read/write
pvedisk mcp setup --guide                  # Print instructions; no files changed
pvedisk mcp setup --client codex           # Register only your installed Codex client
pvedisk mcp setup --client none            # Save server settings only
pvedisk mcp setup --install=false          # Also saves settings without registering clients
pvedisk mcp setup --port 9012 --client none # Change only the saved listener port
```

The default `--client all` configures only detected supported clients. Existing
settings are retained unless explicitly overridden. `--listen` takes a host and
port; an explicit `--port` replaces that port. Setup requires a fixed nonzero
port. `--token` generates a private token if missing and reuses it on reruns;
`--token-file` selects an existing token. `--no-token` clears a saved token-file
selection, while selecting a token file clears saved tokenless mode. Explicit
boolean values such as `--no-token=false` override saved values too.

Client registration updates the `pvedisk` entry and preserves unrelated settings.
Changed files receive private byte-exact backups. JSONC comments are preserved;
Codex TOML is normalized on writing, so comments and formatting may change, with
the original retained in its backup. Authenticated registrations store the
bearer header in the private client configuration; its backups are credentials
too. A client registration error reports that
the server settings were saved and identifies the client that needs attention.

Local registration uses the listener address and port; wildcard listeners use
loopback for clients on the same host. For a client elsewhere, use `--guide`:
it shows your saved or selected listener/authentication settings and a remote
host placeholder or an SSH-forwarding route. Apply any guide-only overrides
with setup before starting the server. Transfer tokens privately and use the
appropriate URL in the remote account's client configuration.

This setup flow follows the current QMP pattern. See the
[shared-package follow-up design](mcp-shared-package.md) for the planned
deduplication; this change does not extract a module or modify QMP.

## Listen and authentication modes

The fresh loopback default accepts clients without a bearer token. For your
isolated LAN, explicitly select a non-loopback listener and tokenless mode:

```sh
pvedisk mcp --listen 0.0.0.0:8766 --no-token
```

Connect your MCP client's HTTP transport to
`http://YOUR-PROXMOX-HOST:8766/mcp`. This endpoint serves MCP, so use your client's
MCP connection settings rather than treating it as a filesystem URL. Returned
filesystem paths are on your Proxmox host, not on the client machine.

To save authenticated LAN settings and create or reuse a private token:

```sh
pvedisk mcp setup --client none --listen 0.0.0.0:8766 --token
pvedisk mcp
```

Configure your client to send `Authorization: Bearer <token-file contents>` on
its MCP requests. The file must be a regular file owned by the server's user,
with permissions `0600` or `0400`. Its token must contain at least 32 printable
ASCII characters without whitespace; one trailing newline is allowed.

`--token-file` and `--no-token` are mutually exclusive. A non-loopback listener
requires one of them. Transport is plain HTTP, including bearer headers.
Tokenless LAN mode allows reachable clients to use all the tools listed below;
bearer authentication requires the configured token for those same tools.

## Tools

| Tool | Input | Behavior |
| --- | --- | --- |
| `list_vms` | None | List this node's VMs and repair restrictions. |
| `list_sessions` | None | List saved sessions and current mount/worker checks. |
| `status` | `vmid` | Inspect one saved session and its current mounts. |
| `ps` | None | List confirmed running mount workers and counts of other records. |
| `version` | None | Show the server executable's build metadata. |
| `doctor` | None | Check host runtime dependencies without changing VMs. |
| `mount` | `vmid`, optional `read_only` | Stop and protect the VM, then expose its guest filesystems. |
| `unmount` | `vmid` | Finalize writes and clean up before releasing owned protection. |
| `clean` | `vmid` | Reconcile an interrupted session and retain protection if cleanup is uncertain. |

Mounting is writable by default. For read-only inspection, call the `mount`
tool with inputs such as `{"vmid": 400, "read_only": true}`. **Read-only mounting
requires an already stopped VM and keeps it stopped and locked.** A running
guest could otherwise change the disks underneath the filesystem view. Use a
separate snapshot or backup if the original guest must stay running.

The tools use the same lifecycle checks, disk discovery, session ownership, and
cleanup safeguards as the CLI. Mount requests reject existing locks, HA-managed
guests, suspended guests, pending disk changes, and occupied destinations.
Running VMs are refused with a `qm shutdown VMID` instruction before any stale
session cleanup. A stopped VM with a recorded, exited worker is automatically
reconciled before mounting; cleanup retains protection if any ownership or disk
usage check fails. Active and pending sessions still require explicit cleanup.

A canceled request, client disconnect, or MCP server restart can leave a detached
mount worker running. Reconnect and inspect `status`, `list_sessions`, or `ps`
before retrying a mount. Use `unmount` when finished, or `clean` if its worker
has exited. The server never starts a guest automatically. See
[cleanup](cleanup.md) when cleanup cannot complete.

Setup and password helpers are not exposed as MCP tools.

## Optional systemd service

The repository includes [pvedisk-mcp.service](../deploy/pvedisk-mcp.service),
which explicitly loads `/etc/pvedisk/mcp.yaml`. Create that file before enabling
the service. For your isolated tokenless LAN, run:

```sh
pvedisk mcp setup --mcp-config /etc/pvedisk/mcp.yaml --client none --listen 0.0.0.0:8766 --no-token
```

Use `--token` instead of `--no-token` to generate or reuse the private token at
`/etc/pvedisk/mcp.token`. Copy the service file to your host and install manually:

```sh
install -m 0644 /path/to/pvedisk-mcp.service /etc/systemd/system/pvedisk-mcp.service
```

Before enabling it, edit `ExecStart` if you want a different executable path,
site configuration file, or MCP settings path. Change listener/authentication
settings with `mcp setup --mcp-config /etc/pvedisk/mcp.yaml`. Then run:

```sh
systemctl daemon-reload
systemctl enable --now pvedisk-mcp.service
systemctl status pvedisk-mcp.service --no-pager
```

To restart the server or inspect its logs:

```sh
systemctl restart pvedisk-mcp.service
journalctl -u pvedisk-mcp.service -n 50 --no-pager
```

Keep `KillMode=process`: only the MCP server should receive the service stop
signal, so detached repair workers can survive its restart. Keep the shared
host mount namespace and host temporary-file visibility. Adding `PrivateMounts`,
`PrivateTmp`, `ProtectSystem`, or other private filesystem namespaces can hide
the public mounts or interfere with native appliance access and cleanup.
Restarting this service does not unmount your guest disks; inspect saved
sessions after reconnecting.

The installer does not enable this service or open a listener automatically.
