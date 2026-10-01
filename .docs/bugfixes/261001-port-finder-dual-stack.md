# Port finder dual-stack bugfixes

- [x] network/port/port.go, used by internal/config.go to choose a user's
  manager port range (ManagerPort/ManagerWeb defaults), probes candidate ports
  on IPv4 only, while the manager binds dual-stack
  (jobqueue/port_reservation*.go reserves on [::] with IPV6_V6ONLY off since
  #652). On a host where an IPv6-only listener holds a port (this host:
  rpc.statd on [::]:45993), the finder can choose a port the manager then
  can't bind, and the manager fails to start ("in use by another process").
  Also check cmd/status_test.go freeStatusTestPorts (IPv4-only pick, bind
  retried) and any other production port-choosing code for the same pattern.
  - Red command: `GOFLAGS=-p=2 GOCACHE=/tmp/claude-11346/gocache-portfinder
    nice -n 19 go test ./network/port/ -run TestRedIPv6OnlyListenerPortOffered
    -count=1` (scratch test network/port/port_dualstack_red_test.go: listens
    on [::]:0 with IPV6_V6ONLY=1, then asks `NewChecker("localhost")` to
    `claimRange` that port). Exit 1:

    ```
    --- FAIL: TestRedIPv6OnlyListenerPortOffered (0.00s)
        port_dualstack_red_test.go:34: checker claimed port 40271 held by an
        IPv6-only listener (checker addr 127.0.0.1:0)
    FAIL
    ```
  - Other port pickers checked: client/testing/server.go already filters
    freeport picks through a dual-stack `portCanListen`; jobqueue listeners
    bind dual-stack; cmd/status_test.go freeStatusTestPorts picks on
    127.0.0.1 only.
  - Fixed: network/port/port.go `NewChecker` now probes candidates on the
    unspecified address, which Go listens on dual-stack (IPv4 alone on a host
    without IPv6), as the manager does; `host` is only checked to resolve.
    network/port/port_test.go `TestPortIPv6OnlyListener` holds a port on
    `[::1]` for IPv6 only and asserts `claimRange` refuses it and
    `AvailableRange` starting there skips it (skips without IPv6 loopback).
    cmd/status_test.go `freeStatusTestPorts` picks on `0.0.0.0:0`.
    CHANGELOG Unreleased/Fixed entry added.
  - Replacement red command: `go test ./network/port/ -run
    TestPortIPv6OnlyListener -count=1` fails on the old port.go
    (port_test.go:337 and :346, "Expected false, Actual true"), passes now.
