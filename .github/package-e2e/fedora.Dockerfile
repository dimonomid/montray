FROM fedora:44

RUN dnf install --assumeyes \
        dbus \
        dbus-daemon \
        dbus-x11 \
        patch \
        systemd \
        xorg-x11-xauth \
        xorg-x11-server-Xvfb \
    && dnf clean all

STOPSIGNAL SIGRTMIN+3
CMD ["/usr/lib/systemd/systemd"]
