# Security

Overall there are three acceptable ways to set up communication between
`montray-ui` and `montray-server`:

1. `montray-ui` -> loopback connection -> locally-running `montray-server` listening on `127.0.0.1` - this is the default and most straightforward setup. No authentication or encryption is needed here, since the communication never leaves the local machine.
2. `montray-ui` -> SSH tunnel to a server -> `montray-server` listening on `127.0.0.1` - this is the easiest setup for a remote server when you have public-key SSH access. Authentication and encryption are delegated to `ssh`; Montray Server itself still doesn't require authentication.
3. `montray-ui` -> TLS connection to a server -> `montray-server` listening on a public interface and requiring bearer-token authentication - TLS provides server authentication and encryption, while the bearer token provides client authentication.

Now let's talk about these in more detail.

## Loopback connection

Not much to say here, it's the default configuration.

The `montray-server` webserver configuration simply listens on a local port without requiring authentication:

```yaml
  messengers:
    - webserver:
        listenAddress: "127.0.0.1:41990"
```

And the Montray UI configuration is equally boring:

```yaml
wsClient:
  servers:
    - id: local # Arbitrary but unique ID for this server.
      addr: localhost:41990
```

## Remote Montray Server via SSH tunnel

This is the easiest setup for a remote server when you have public-key SSH
access. Authentication and encryption are delegated to `ssh`; Montray Server
itself still doesn't require authentication.

The `montray-server` config stays exactly as it is with the loopback connection:
it listens locally without authentication because, from the server's point of
view, the connection is still local.

While the Montray UI configuration should specify the tunnel:

```yaml
    - id: myserver            # Arbitrary but unique ID for this server.
      tunnel:
        ssh:
          host: myserver.com  # TODO: Your actual server hostname
          user: myuser        # TODO: Your actual ssh user
          port: 22            # Change if using non-default ssh port
          remoteServerAddr: 127.0.0.1:41990
```

Notice that the entry has no `addr`. For the built-in SSH tunnel, omitting it tells Montray UI to allocate an available port on `127.0.0.1`. The selected address is written to the log and reused when the tunnel process restarts.

Montray UI then spawns an external `ssh` process forwarding the remote port 41990 to the allocated local port, and connects once the tunnel is ready. The ssh command will be equivalent to this, with the allocated port substituted:

```
ssh -N -T \
  -o BatchMode=yes \
  -o ExitOnForwardFailure=yes -o ConnectTimeout=15 \
  -o ServerAliveInterval=10 -o ServerAliveCountMax=3 \
  -o PermitLocalCommand=yes -o "LocalCommand=echo MONTRAY_TUNNEL_READY" \
  -p 22 \
  -L 127.0.0.1:<allocated-port>:127.0.0.1:41990 \
  myuser@myserver.com
```

You can set an explicit loopback `addr` if you need a stable local port. Note
that custom tunnel commands (described below) must always set one because
Montray UI cannot inject an automatically selected port into an arbitrary command.

If you need to pass some extra arguments to `ssh`, such as to specify a
specific private key to use or anything else, you can specify them using the
`extraSshArgs` field, like that:

```yaml
      tunnel:
        ssh:
          # .... all the field as shown above
          extraSshArgs:
            - "-i"
            - "/path/to/specific/private.key"
```

And they will be added to the ssh command.

You can also establish the tunnel manually if you want, and get the same result, but I find it convenient to let Montray UI manage the tunnel for me.

## Remote Montray Server via custom tunnel command

You could use any custom command to establish a tunnel, like that:

```yaml
    - id: myserver            # Arbitrary but unique ID for this server.
      addr: localhost:42990   # Just any available port on the local machine
      tunnel:
        customCommand:
          command: [
            # Any arbitrary command can go here, like your custom script or whatever.
            # For this example, we again use ssh.
            "ssh",
            "-N", "-T",
            "-o", "BatchMode=yes",
            "-o", "PermitLocalCommand=yes",
            "-o", "LocalCommand=echo MY_TUNNEL_IS_READY",
            # ... whatever other arguments you need
            "-L", "localhost:42990:127.0.0.1:41990",
            "myuser@myserver.com"
          ]
          readinessProbe:
            containsOutput: "MY_TUNNEL_IS_READY"
```

As you see, we're specifying a raw command to be executed, and optionally also a substring to watch for in the output which would mean that the tunnel is ready to use. If the `readinessProbe` isn't provided, the tunnel is considered ready right after command start.

Make sure that the custom tunnel command does not spawn subprocesses, and that when the tunnel is dead, the command should exit.

## Remote Montray Server via TLS and bearer token

This is a bit more involved to set up, so before you go there, make sure you're familiar with the simpler alternatives explained above.

In this setup, `montray-server` listens on a public interface, so both TLS and
authentication should be configured. TLS encrypts the connection and lets
Montray UI verifies the server, while the bearer token lets `montray-server`
authenticate it.

First, let's setup the TLS part.

### Setting up TLS

You need a TLS certificate and its private key on the server. The user running
`montray-server` (named `montray` by the default setup) must be able to read
both files.

If you don't have an existing certificate that you can use, you can create a self-signed one, like that (optionally replace `myserverforcert.com` with whatever hostname you want to use in the certificate, and also adjust the expiration `-days` as you need):

```bash
sudo mkdir -p /etc/montray-server/tls
sudo chown root:montray /etc/montray-server/tls
sudo chmod 0750 /etc/montray-server/tls

sudo openssl req -x509 -newkey rsa:3072 -sha256 -days 3650 -nodes \
  -keyout /etc/montray-server/tls/privkey.pem \
  -out /etc/montray-server/tls/cert.pem \
  -subj "/CN=myserverforcert.com" \
  -addext "subjectAltName=DNS:myserverforcert.com" \
  -addext "basicConstraints=critical,CA:FALSE" \
  -addext "keyUsage=critical,digitalSignature,keyEncipherment" \
  -addext "extendedKeyUsage=serverAuth"

sudo chown root:montray /etc/montray-server/tls/privkey.pem /etc/montray-server/tls/cert.pem
sudo chmod 0640 /etc/montray-server/tls/privkey.pem /etc/montray-server/tls/cert.pem
```

In the end, with a normal certificate or a self-signed one, the
`montray-server` webserver configuration could look like this:

```yaml
  messengers:
    - webserver:
        listenAddress: "0.0.0.0:41990"
        tls:
          certFile: "/etc/montray-server/tls/cert.pem"     # Adjust if needed
          keyFile: "/etc/montray-server/tls/privkey.pem"   # Adjust if needed
```

On the Montray UI side, we need to specify that we want to use TLS. If the server certificate is issued by a CA trusted by your operating system, we just need to add an empty `tls` object to the corresponding server:

```yaml
wsClient:
  servers:
    - id: myserver
      addr: myserver.com:41990
      tls: {}
```

If the certificate was self-signed though, or if hostname in `addr` is different from the hostname in the certificate, you need to specify details, like that:

```yaml
      tls:
        caFile: "/path/to/cert.pem"       # A copy of the self-signed cert.pem
        serverName: myserverforcert.com   # Hostname used in the certificate
```

With that, TLS should be set up now, and we move on to the bearer token.

### Setting up bearer token

Montray UI has a convenient command for this:

```
montray-ui generate-bearer-token myserver
```

Here `myserver` is the ID of the corresponding server in the Montray UI
configuration. The command creates a token file with owner-only permissions and
prints the exact configuration snippets to add on both sides. Montray UI stores
the token itself, while `montray-server` stores only its SHA-256 hash.

As the command output tells us, we need to add the `auth` object to the Montray UI config:

```yaml
wsClient:
  servers:
    - id: myserver
      addr: myserver.com:41990
      tls: {} # Or whatever you had there
      auth:
        bearerTokenFile: "/path/to/myserver.token"
```

And on the server side, also add `auth` with the corresponding token hash, so it ends up looking like this:

```yaml
  messengers:
    - webserver:
        listenAddress: "0.0.0.0:41990"
        tls:
          certFile: "/path/to/cert.pem"
          keyFile: "/path/to/privkey.pem"

        auth:
          - id: my-laptop # Identifies this credential; adjust as needed.
            bearerTokenHash: "sha256:..."
```
