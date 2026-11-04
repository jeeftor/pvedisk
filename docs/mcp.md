# HTTP MCP server

Run `pvedisk mcp` on your Proxmox host to expose inspection and mount operations
to an HTTP MCP client. Run host operations as root. The endpoint is `/mcp` and
the default listener is `127.0.0.1:8766`.

```sh
pvedisk mcp
```

Starting the server does not run setup, load your site configuration, or run
doctor. The `mount` tool requires valid configuration when called. Select a
fixed configuration file for that server process with the global flag:

```sh
pvedisk --config /etc/pvedisk/config.yaml mcp
```

Read-only tools, unmount, and recovery continue to work without valid site
configuration because they use your host inventory or saved session paths.

## Listen and authentication modes

The default loopback listener accepts clients without a bearer token. For your
isolated LAN, explicitly select a non-loopback listener and tokenless mode:

```sh
pvedisk mcp --listen 0.0.0.0:8766 --no-token
```

Connect your MCP client's HTTP transport to
`http://YOUR-PROXMOX-HOST:8766/mcp`. This endpoint serves MCP, so use your client's
MCP connection settings rather than treating it as a filesystem URL. Returned
filesystem paths are on your Proxmox host, not on the client machine.

To require a bearer token, create a private token file as root. The following
command creates a random token and refuses to overwrite an existing file:

```sh
install -d -o root -g root -m 0750 /etc/pvedisk
(umask 077; set -C; openssl rand -hex 32 > /etc/pvedisk/mcp.token)
pvedisk mcp --listen 0.0.0.0:8766 --token-file /etc/pvedisk/mcp.token
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
| `recover` | `vmid` | Reconcile an interrupted session and retain protection if cleanup is uncertain. |

Mounting is writable by default. For read-only inspection, call the `mount`
tool with inputs such as `{"vmid": 400, "read_only": true}`. **Read-only mounting
still shuts down a running VM and keeps it stopped and locked.** A running
guest could otherwise change the disks underneath the filesystem view. Use a
separate snapshot or backup if the original guest must stay running.

The tools use the same lifecycle checks, disk discovery, session ownership, and
cleanup safeguards as the CLI. Mount requests reject existing locks, HA-managed
guests, suspended guests, pending disk changes, and occupied destinations.
Graceful shutdown has a 120-second limit and never forces the guest off.

A canceled request, client disconnect, or MCP server restart can leave a detached
mount worker running. Reconnect and inspect `status`, `list_sessions`, or `ps`
before retrying a mount. Use `unmount` when finished, or `recover` if its worker
has exited. The server never starts a guest automatically. See
[recovery](recovery.md) when cleanup cannot complete.

Setup and password helpers are not exposed as MCP tools.

## Optional systemd service

The repository includes [pvedisk-mcp.service](../deploy/pvedisk-mcp.service).
It intentionally listens on your LAN at `0.0.0.0:8766` with `--no-token`.
Copy the file to your host and install it manually:

```sh
install -m 0644 /path/to/pvedisk-mcp.service /etc/systemd/system/pvedisk-mcp.service
```

Before enabling it, edit `ExecStart` if you want a different executable path,
configuration file, listener, or `--token-file` instead of `--no-token`. Then run:

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
the public mounts or interfere with native appliance access and recovery.
Restarting this service does not unmount your guest disks; inspect saved
sessions after reconnecting.

The installer does not enable this service or open a listener automatically.
