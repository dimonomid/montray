FROM debian:bookworm-slim

RUN apt-get update \
    && apt-get install --yes --no-install-recommends \
        dbus \
        dbus-daemon \
        dbus-x11 \
        patch \
        systemd \
        xauth \
        xvfb \
    && rm -rf /var/lib/apt/lists/*

STOPSIGNAL SIGRTMIN+3
CMD ["/lib/systemd/systemd"]
