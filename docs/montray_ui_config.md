# Configuring Montray UI

The default config file is `$XDG_CONFIG_HOME/montray-ui/montray-ui.yml`, or `~/.config/montray-ui/montray-ui.yml` when `XDG_CONFIG_HOME` isn't set.

When that file does not exist, Montray UI also reads the former
`$XDG_CONFIG_HOME/salmon-watch/salmon-watch.yml` path for compatibility.

To use another one:

```
$ montray-ui --config /somewhere/montray-ui.yml
```

YAML is parsed strictly. An unknown key causes an error instead of being silently ignored.

## Servers

The smallest useful config is:

```yaml
wsClient:
  servers:
    - id: local
      addr: localhost:41990
```

Every server has these fields:

### `id`

An arbitrary unique ID containing letters, digits, underscores, or hyphens. `internal` is reserved by Montray UI.

The ID prefixes every incident received from that server, so it shouldn't be changed casually after the setup is already in use. Among other things, changing it also changes the keys used by saved snoozes.

### `addr`

The address Montray UI connects to, in `host:port` form. With a tunnel, this is the local end of that tunnel, not the remote SSH host.

For a structured `tunnel.ssh`, `addr` may be omitted. Montray UI then asks the operating system for an available port on `127.0.0.1` and uses it for both the SSH forwarding listener and its WebSocket connection. The address is allocated when Montray UI starts, logged for diagnostics, and reused if that SSH tunnel is restarted. To require a stable local forwarding port instead, set an explicit loopback address such as `127.0.0.1:42990`.

Automatic port allocation applies only to the built-in, structured `tunnel.ssh` form. Direct connections require the remote server address, and `tunnel.customCommand` requires an explicit local `addr` that also appears in, or is otherwise understood by, the custom command.

The SSH destination is configured as `remoteServerAddr`. Configurations from
before the Montray rename may still use `remoteSalmonAddr`; that spelling
remains accepted for compatibility.

### Authentication

The simple config above is good for local monitoring, but for remote machines
this isn't enough. Montray UI and Montray Server support SSH tunnels, TLS, and
bearer-token authentication; see [Security](./security.md) for details.
