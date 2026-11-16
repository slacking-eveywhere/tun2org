# Benchmarks: ntun-ng vs legacy tun2org

Goal: quantify the perf wins from removing gob reflection, the double-SOCKS5
hop, and the 1024-byte read cap.

## Method

- Target: `iperf3 -s` on the forward server (loopback on the SIS host, so the
  path under test is workstation → relay → forward server → iperf3).
- Client: `iperf3 -c <target> -t 15 -P 4` (4 parallel streams, 15s).
- Capture: throughput (Mbps) and host CPU% (iperf3 JSON).
- Each variant brought up/down with its own config; fresh run between variants.
- Record results in `benchmarks.tsv` (throughput_mbps, cpu_pct per run).

## Expected wins (design rationale)

| Change                   | Legacy cost            | ntun-ng                       |
| ------------------------ | ---------------------- | ----------------------------- |
| gob reflection per frame | high CPU per 1024 B    | binary framing, ~0 reflection |
| 1024 B read cap          | many tiny frames       | 64 KiB DATA frames            |
| localhost SOCKS5 hop     | extra copy + handshake | direct mux → agent net.Dial   |
| double SOCKS5            | two handshakes         | target addr in OPEN, one dial |
| armon/go-socks5 remote   | extra layer            | agent dials directly          |

Expected: large throughput increase + lower CPU under load; reconnect storms
gone (agent cached + PING/PONG detection). Latency should drop (fewer copies).

## Captured results

> Fill in after running the iperf3 method above in your env. Paste the `benchmarks.tsv`
> table here.

```
variant   throughput_mbps   cpu_pct
legacy    ...               ...
ntun-ng   ...               ...
```

## Notes

- The single SSH session stdin/stdout is the fundamental throughput ceiling;
  phase-deferred `sessions N` striping would lift it (adds relay load).
- TCP buffer tuning (`tcp_sndbuf`/`tcp_rcvbuf`/`tcp_auto_tuning` in config)
  can be swept to find the sweet spot for the relay's RTT.
