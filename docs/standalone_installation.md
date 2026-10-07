# Standalone Montray installation

Use the standalone installation if the prebuilt packages do not work on your
Linux distribution.

First, download the
[latest release archives from GitHub](https://github.com/dimonomid/montray/releases/latest),
such as `montray-server-x.y.z_linux_amd64.tar.gz` and
`montray-ui-x.y.z_linux_amd64.tar.gz`, and unpack them. You'll get two binaries:
`montray-server` and `montray-ui`.

Then:

```bash
# Set up the monitoring service and start it. This also installs montray-server
# under /usr/local/bin when not already there.
sudo ./montray-server setup

# Install the desktop application system-wide:
sudo install -m 755 montray-ui /usr/local/bin/montray-ui

# Let the desktop application create its default config, autostart entry,
# and application launcher:
montray-ui setup

# Start the desktop application (Montray Server is already running):
montray-ui
```

You should now see a tray icon, and if you click on it, you'll see the UI. When
you reboot, it will start automatically.
