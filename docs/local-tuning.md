# Local tuning for load tests

Single-box k6-against-localhost runs are clean up to a few thousand rps. Past
that, the *load generator and the OS* become the bottleneck before the API does,
so numbers stop reflecting the server. This note lists the knobs to raise that
ceiling and when to move k6 to a second machine.

## File descriptors

Each open connection (k6 client sockets + server accepted sockets + pgx pool)
costs a file descriptor. The default soft limit (often 1024) is hit well before
1K rps with keep-alive churn.

```bash
ulimit -n            # check current soft limit
ulimit -n 65535      # raise for this shell (both k6 and server shells)
```

Persist system-wide in `/etc/security/limits.conf`:

```
*  soft  nofile  65535
*  hard  nofile  65535
```

## Ephemeral ports (TCP source-port exhaustion)

When k6 and the server share one host, every request pair consumes an ephemeral
port on the loopback. Under high rps with short-lived connections, ports sit in
`TIME_WAIT` and the range drains — you'll see `connect: cannot assign requested
address` errors that look like server failures but are OS limits.

```bash
sysctl net.ipv4.ip_local_port_range        # default often 32768 60999 (~28k)
sudo sysctl -w net.ipv4.ip_local_port_range="1024 65535"
sudo sysctl -w net.ipv4.tcp_tw_reuse=1     # reuse TIME_WAIT sockets for outbound
```

Mitigate churn first: k6 reuses connections by default — keep
`noConnectionReuse: false` (the default) so you are not minting a port per
request.

## When to split k6 onto a second machine

Rule of thumb: **above ~5K rps, run k6 on a separate host from the API.** On one
box the two processes contend for the same CPUs, NIC, and port range, so a red
run can mean "the laptop ran out of ports," not "the server broke."

- Same region / low-latency link so network RTT doesn't dominate the p95.
- Size the k6 box at least as large as the API box; a starved generator
  under-reports throughput.
- Point k6 at the API's real address: `make load BASE_URL=http://<api-host>:8000 RPS=5000`.
- Watch both sides: API CPU + `pgxpool` stats on the server, CPU + socket counts
  (`ss -s`) on the k6 host. Whichever saturates first is your real ceiling.

## Observed local ceiling

Rungs 1–3 (1 / 10 / 100 rps) run clean on a single 12-core box with defaults;
pgxpool never contended (see `docs/benchmark-logbook.md`). Re-measure the local
ceiling when pushing toward 1K–10K rps (rung 4+); the first symptom is usually
port/FD exhaustion on the shared host, which is the trigger to go two-machine
(and eventually cloud, per the ladder).
