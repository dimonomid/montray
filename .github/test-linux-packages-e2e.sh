#!/usr/bin/env bash

# Install the Debian and Fedora packages on clean systems with systemd, then
# check that the packaged server and UI can pass the end-to-end test. Migration
# from an old manual install is tested by test-linux-packages.sh.

set -euo pipefail

release_dir="$(realpath "${1:?release directory is required}")"
repo_dir="$(git rev-parse --show-toplevel)"

run_test() {
  local image="$1"
  local dockerfile="$2"
  local package_command="$3"
  local container_id

  docker build \
    --tag "$image" \
    --file ".github/package-e2e/${dockerfile}.Dockerfile" \
    .
  container_id="$(docker run --detach --privileged \
    --cgroupns=host \
    --security-opt seccomp=unconfined \
    --volume "${release_dir}:/release:ro" \
    --volume "${repo_dir}:/workspace:ro" \
    --tmpfs /run \
    --tmpfs /run/lock \
    --volume /sys/fs/cgroup:/sys/fs/cgroup:rw \
    "$image")"
  cleanup() {
    docker stop "$container_id" >/dev/null 2>&1 || true
    docker rm "$container_id" >/dev/null 2>&1 || true
  }
  trap cleanup RETURN

  systemd_state=starting
  for _ in {1..60}; do
    systemd_state="$(docker exec "$container_id" systemctl is-system-running 2>/dev/null || true)"
    case "$systemd_state" in
      running|degraded) break ;;
    esac
    sleep 1
  done
  if [[ "$systemd_state" != running && "$systemd_state" != degraded ]]; then
    echo "systemd did not start in the test container (state: $systemd_state)" >&2
    docker inspect "$container_id" --format 'status={{.State.Status}} exit={{.State.ExitCode}} error={{.State.Error}}' >&2 || true
    docker logs "$container_id" >&2 || true
    return 1
  fi
  docker exec "$container_id" dbus-daemon --system --fork

  docker exec "$container_id" bash -euo pipefail -c "
    $package_command
    for binary in /usr/bin/montray-server /usr/bin/montray-ui; do
      if output=\$(\"\$binary\" setup 2>&1); then
        echo \"\$binary setup unexpectedly succeeded\" >&2
        exit 1
      fi
      grep -Fq \"in-app setup is disabled\" <<<\"\$output\"
      echo \"Confirmed: \$binary setup is disabled\"
    done
    patch --batch --forward /etc/montray-server.yml /workspace/.github/e2e/montray-server.yml.patch
    systemctl restart montray-server.service
    systemctl is-active --quiet montray-server.service

    mkdir -p /tmp/montray-ui-home
    export HOME=/tmp/montray-ui-home
    export XDG_CONFIG_HOME=/tmp/montray-ui-home/.config
    export XDG_DATA_HOME=/tmp/montray-ui-home/.local/share
    ui_log=/tmp/montray-ui.log
    setsid dbus-run-session -- xvfb-run -a /usr/bin/montray-ui --start-hidden >\"\$ui_log\" 2>&1 &
    ui_pid=\$!

    for _ in {1..120}; do
      if grep -Fq \"UI and system tray initialized\" \"\$ui_log\" &&
        grep -Fq \"server local connected\" \"\$ui_log\" &&
        grep -Fq \"server local synchronized:\" \"\$ui_log\" &&
        grep -Eq \"server local (ongoing|added|updated) incident e2e\\.exec_result \\(error\\): .*expected-e2e-incident\" \"\$ui_log\"; then
        echo \"Received expected incident in the packaged UI\"
        exit 0
      fi
      if ! kill -0 \"\$ui_pid\" 2>/dev/null; then
        break
      fi
      sleep 0.25
    done

    cat \"\$ui_log\"
    journalctl --no-pager -u montray-server.service
    exit 1
  "
}

run_test montray-package-e2e-debian debian \
  'apt-get update && apt-get install --yes --no-install-recommends /release/montray-server_*.deb /release/montray-ui_*.deb'

run_test montray-package-e2e-fedora fedora \
  'dnf install --assumeyes /release/montray-server-*.rpm /release/montray-ui-*.rpm'
