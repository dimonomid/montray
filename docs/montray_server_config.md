# Configuring Montray Server

The default config file is `/etc/montray-server.yml`. To use another one:

```
$ montray-server --config /somewhere/montray-server.yml
```

During normal startup, Montray Server falls back to the former
`/etc/salmon.yml` path when the new default does not exist. Fresh setup always
creates the new path.

YAML is parsed strictly. An unknown key causes an error instead of being silently ignored.

The top level looks like this:

```yaml
core:
  collectors:
    # ...
  messengers:
    # ...
```

Collectors check the machine and report items. Messengers publish the resulting incident changes.

## Collectors

Collectors check the machine and report items.

Every collector has a string `id`: just an arbitrary but unique string with no special meaning, will be included in incident keys.

### Systemd collector

The systemd collector watches systemd units over D-Bus. Its main configuration is an ordered list of rules:

```yaml
- id: systemd   # Arbitrary but unique string, will be included in incident keys
  systemd:
    unitRules:
      # For some most important services on the machine (such as e.g. nginx if
      # the main purpose of the machine is to serve web traffic), require them to
      # be exactly active, and treat any other state as an error.
      - names: [important.service, another-important.service]
        conditions:
          - {state: active, result: ok}
          - {result: error}

      # Catchall rule for all services: treat normal lifecycle rules as ok, but
      # anything outside of that is a warning.
      - type: service
        conditions:
          - {
              subStateContains: auto-restart,
              result: warning,
              resolve: {after: 5s, states: [active, inactive, not-sent-by-systemd]}
            }
          - {state: active, result: ok}
          - {state: inactive, result: ok}
          - {state: activating, result: ok}
          - {state: deactivating, result: ok}
          - {state: reloading, result: ok}
          - {state: not-sent-by-systemd, result: ok}
          - {result: warning}
```

Rules are checked in order, and the first matching rule wins. Put special cases before broad rules.

Let's look at the structure of every rule in `unitRules`

#### Rules in `unitRules`

Every rule has matchers and conditions.

The logic for all specified matchers is `AND`, so all of them must match for the rule to take effect. Possible matchers are:

- `names`: If present, this rule would only match one of the systemd unit names mentioned here. For example, `[nginx.service, some-other.service]`
- `type`: If present, this rule would only match the units of this type. For example, `service`

And conditions are listed as the `conditions` array, where items are checked in order, and the first matching condition wins. If there were no matching `conditions`, the `error` result is assumed.

##### Conditions in `unitRules[N].conditions`

Every condition has its own matchers and result.

The logic for all specified matchers is `AND`, so all of them must match for the rule to take effect. Possible matchers within the condition are:

- `state`: If present, checks the systemd's unit state. Examples: `active`, `inactive`, `failed` etc. Check systemd documentation for the possible states. **NOTE**: Montray Server supports one extra synthetic state: `not-sent-by-systemd`. As the name suggests, we consider an item to be in this state when systemd doesn't report this unit at all.
- `subState`: If present, checks the systemd's unit substate. Examples: `auto-restart`, `dead-before-auto-restart`, `auto-restart-queued`, etc. Check systemd documentation for the possible substates.
- `subStateContains`: If present, checks the systemd's unit substate for a matching substring. So e.g. `auto-restart` would match all these states: `auto-restart`, `dead-before-auto-restart`, `auto-restart-queued`.

And a condition must contain `result`: the resulting item state. Possible states are: `ok`, `warning`, `error`. A non-`ok` state creates an incident.

Normally, the incident is resolved once that item enters an `ok` state again, but in some cases we want to wait more to get more confidence, e.g. when an item is auto-restarting and briefly does enter a regular `active` state. To facilitate that, there is also an optional `resolve` object:

###### Incident resolution constraints in `unitRules[N].conditions[M].resolve`

It has two fields, both are required:

- `states`: Specifies the states which are considered "exit" states from an incident. If a unit enters some other state not mentioned here, the incident would remain active even if normally other conditions don't treat it as an error. For example, it often makes sense to specify stable states, such as `active`, `inactive` and the pseudo-state `not-sent-by-systemd`.
- `after`: Specifies minimum duration that the aforementioned states are active without interruption. E.g. if we specify `states` to be `[active, inactive]`, and then `after` is 5s, and the unit enters `active` state: first we just start the timer and wait for 5s, but the incident stays. If after 1s the unit changes state to some other state (neither `active` nor `inactive`), the timer stops and the incident will stay, waiting for more state changes. After 5s spent in any of these specified states, the incident finally resolves.

This helps to avoid reoccurring incidents (and their notifications) if a service keeps failing and restarting.

### Exec collector

The exec collector runs an arbitrary command periodically and turns its exit code into an item state:

```yaml
- id: root-free-space   # Arbitrary but unique string, will be included in incident keys
  exec:
    description: Root filesystem free space
    command:
      - sh
      - -c
      - |
        minimum_mib=100
        available_mib="$(df -Pm / | awk 'NR == 2 { print $4 }')" || exit 1
        if [ "$available_mib" -lt "$minimum_mib" ]; then
          echo "only $available_mib MiB free on /; want at least $minimum_mib MiB."
          exit 1
        fi
```

The command runs immediately when Montray Server starts, and then periodically as per poll interval settings described below.

Every exec collector can contain the following possible fields:

#### `description`

Optional text placed before the dynamic command result in incident details.

#### `command`

An executable followed by its arguments. Montray Server runs it directly, without a shell. If you need shell syntax such as pipes, redirects, variables, or `if`, use shell explicitly like `sh -c` as shown above.

The first line of stdout is used as the dynamic incident details. If stdout is empty, Montray Server uses `exit code: N` instead.

Only the first 200 bytes are kept. Montray Server still reads and discards the rest, so a command which prints a lot of output won't block or consume unlimited memory.

#### `pollInterval`

How often to run the command while it is healthy. Default: `1m`.

#### `pollIntervalWhenUnhealthy`

How often to run it while it reports a warning or error. Default: `5s`.

#### `timeout`

The maximum duration of one execution. By default, it's the shortest of `1m`, `pollInterval`, and `pollIntervalWhenUnhealthy`.

An explicitly configured timeout must not be longer than either polling interval. Otherwise one execution could still be running when the next one is supposed to begin, and Montray Server doesn't try to support overlapping executions of the same check.

#### `conditions`

Optional exit-code mapping. When omitted, the default is equivalent to:

```yaml
conditions:
  - {exitCode: "0", result: ok}
  - {result: error}
```

Which means the straightforward rule "exit code 0 is ok, everything else is an error"

Exit codes are strings in YAML. The first matching condition wins, and omitting `exitCode` makes an unconditional fallback.

For example, to treat exit code 2 as a warning:

```yaml
conditions:
  - {exitCode: "0", result: ok}
  - {exitCode: "2", result: warning}
  - {result: error}
```

## Messengers

Messengers publish the resulting incident changes.

### File logger messenger

To write incident transitions to stdout:

```yaml
- fileLogger:
    fileName: ""
```

To append them to a file:

```yaml
- fileLogger:
    fileName: /var/tmp/montray-server-incidents.log
```

This is separate from Montray Server's application logs. The file logger only records incidents appearing, changing, and going away.

### Webserver messenger

```yaml
- webserver:
    listenAddress: "127.0.0.1:41990"
```

The default loopback address is intentional. Keep it that way for normal local and SSH-tunnel setups; see [Security](./security.md).
