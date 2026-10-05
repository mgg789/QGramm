# Scenario benchmark results — 2026-10-05

45 real comparative/diagnostic attempts, including four attachment runs. Original failed and superseded attempts are retained. Selected messaging runs verify 4,679,617 live deliveries with no missing or duplicate IDs; 32 of 35 selected non-file attempts (including idle and consumer-state diagnostics) also pass full history integrity. The two failing Centrifugo 4 KiB history runs are explicitly identified below. These totals do not qualify an untested environment or every target rate.

Reproduction: [suite and runner](../tools/scenarios/README.md). Evidence: [all phase rows](benchmarks/scenarios/summary.json), [totals](benchmarks/scenarios/selected-totals.json), [initial source/image provenance](benchmarks/scenarios/campaign-provenance.json), [generator v2 provenance](benchmarks/scenarios/instrumentation-v2-provenance.json). Raw JSON names in the last table are relative to `docs/benchmarks/scenarios/`. CPU100% is one core; RAM is sampled working set.

## What the scenarios reveal

At 500 original messages/s across 1,000 conversations, QGramm accepted all planned work in both runs: delivery p95 2.50–2.55 ms, p99 4.89–5.56 ms, mean CPU 28.6% and sampled working set 89–97 MiB. Centrifugo is faster with lower CPU and volatile history. NATS FILE/FILE uses less CPU but more memory and has more variable tails. These are different application contracts.

At 3,000/s with 256-byte payloads QGramm accepts 93–96% of planned work, with explicit 503 backpressure and successful-delivery p99 55–58 ms. NATS FILE/FILE cannot maintain the open-loop schedule; Centrifugo accepts nearly all of it. Always read successful tails together with admission/skips. Raw phase and pending data include the recovery to 100/s.

For 100 original messages/s × 100 recipients, QGramm completes all 330,000 expected deliveries per entire group run. High-step delivery p99 is 13.43–16.09 ms, CPU about 1.58 cores and sampled RAM about 93 MiB. Centrifugo p99 is 3.77–3.79 ms. This measures individual recipient deliveries, not each message's last-recipient completion or durable all-device receipts.

The separate AB/BA NATS control gives FILE/FILE high-step delivery p99 6,595–6,859 ms versus 3.16–3.28 ms with FILE stream and MEMORY consumers; both MEMORY repeats accept and deliver every planned message. The isolated FILE-stream/MEMORY-consumer control measures sensitivity to ACK state storage. It remains a within-NATS diagnostic and provides no server-restart state guarantee; QGramm's fixture does not submit persisted delivered/read receipts.

Following a five-second outage of 100 recipients, QGramm drains 1,000 missing deliveries in 105–115 ms from reconnect initiation. NATS takes 56–61 ms and Centrifugo 27–30 ms. These timings include new connections; origin latency separately includes the intentional outage.

With 4 KiB payloads, QGramm accepts 1,500/s completely in both corrected repeats, delivery p99 16.87–26.15 ms. At 3,000/s it accepts 65–81%, with explicit backpressure and a few generator deadline misses. Both corrected Centrifugo runs complete exact live delivery but are OOM-killed during full history verification; their live rows do not qualify whole-run integrity.

## Longer load and attachments

At 500/s for 300 seconds QGramm accepts 149,887 of 150,000 planned operations, with 62 explicit 503 rejects and 51 generator deadline skips. Delivery p95/p99 is 3.91/10.35 ms; all accepted messages pass delivery/history integrity, but the strict load SLO fails. NATS FILE/FILE accepts 132,301 with 17,699 skips and delivery p95/p99 627.35/2,098.16 ms; all accepted messages also pass full history integrity. Mean sampled RAM in the first/last approximately 30 seconds is 96.7/145.9 MiB for QGramm and 261.5/379.8 MiB for NATS. Five minutes cannot establish a memory leak.

All four attachment runs pass six checks: eight files total 320 MiB plaintext. Two concurrent 64 MiB files upload at 56.26–58.35 MiB/s per file, download at 97.64–100.79 MiB/s, and complete the entire flow in 1.88–1.92 seconds. The flow includes checks and retries, not just byte transfer.

Centrifugo accepts 149,979 messages over 300 seconds, with 21 skipped deadlines and no rejects; delivery p95/p99 is 0.94/1.41 ms. Live delivery and history are complete. First/last15-sample mean RAM is 167.1/290.0 MiB. The strict SLO fails because of scheduler skips; all three services pass integrity for accepted work.

At 10,000 established connections, the separate QGramm retry has settled idle mean CPU 0.6% and sampled RAM 237.2 MiB; NATS 0.2%/555.8 MiB and Centrifugo 8.1%/426.2 MiB. These are the last five idle samples and one verified exchange, not 10k active traffic. QGramm’s first setup attempt failed to establish all connections; a separately named retry after a 60-second cooldown passed with the same generator/server. The first failure’s cause is not established.

## Next useful measurements

Repeat identical versions, configurations and schedules on a dedicated Linux/local-SSD host with enough available memory, including the failed 4 KiB history gate. Then isolate HTTPS/WSS, equal payload/conversation-count pairs, batches versus individual sends, persisted device receipts and simultaneous messaging/files. Server restart/crash recovery and MLS need separate explicit contracts. These results do not justify a new GOGC or weakening FULL, ACL or cryptography.


## distributed: 500 original messages/s

| Service | Accepted/planned % | ACK p95 ms | ACK p99 ms | Delivery p95 ms | Delivery p99 ms | CPU % | RAM MiB | Skipped | Rejected | Run integrity |
| --- | --- | --- | --- | --- | --- | --- | --- | --- | --- | --- |
| qgramm | 100.00 | 2.41–2.44 | 4.70–5.52 | 2.50–2.55 | 4.89–5.56 | 28.6–28.6 | 89.2–96.5 | 0 | 0 | True |
| nats | 100.00 | 2.00–8.27 | 7.10–49.29 | 2.02–10.97 | 9.11–51.99 | 17.0–17.0 | 261.7–264.6 | 0 | 0 | True |
| centrifugo | 100.00 | 0.89–0.98 | 1.25–1.73 | 0.91–1.00 | 1.26–1.74 | 9.4–9.6 | 128.5–169.8 | 0 | 0 | True |

## distributed: 1500 original messages/s

| Service | Accepted/planned % | ACK p95 ms | ACK p99 ms | Delivery p95 ms | Delivery p99 ms | CPU % | RAM MiB | Skipped | Rejected | Run integrity |
| --- | --- | --- | --- | --- | --- | --- | --- | --- | --- | --- |
| qgramm | 99.85–99.98 | 9.25–9.47 | 19.58–22.44 | 10.07–10.34 | 21.11–24.10 | 81.3–81.8 | 89.2–93.7 | 7–44 | 0 | True |
| nats | 39.46–54.19 | 439.07–691.03 | 564.67–921.44 | 440.37–692.06 | 565.30–922.82 | 19.6–23.6 | 287.6–297.0 | 13743–18162 | 0 | True |
| centrifugo | 99.99–100.00 | 0.70–0.83 | 1.35–1.45 | 0.71–0.85 | 1.36–1.47 | 11.1–12.4 | 160.8–195.2 | 1–2 | 0 | True |

## distributed: 3000 original messages/s

| Service | Accepted/planned % | ACK p95 ms | ACK p99 ms | Delivery p95 ms | Delivery p99 ms | CPU % | RAM MiB | Skipped | Rejected | Run integrity |
| --- | --- | --- | --- | --- | --- | --- | --- | --- | --- | --- |
| qgramm | 93.07–95.97 | 39.55–42.41 | 52.93–56.13 | 41.08–43.91 | 55.30–57.77 | 165.9–168.7 | 124.0–126.1 | 2–3 | 2412–4155 | True |
| nats | 23.99–26.20 | 494.53–581.50 | 590.34–627.80 | 495.90–582.68 | 592.58–628.58 | 26.2–26.5 | 303.4–308.8 | 44278–45606 | 0 | True |
| centrifugo | 99.99–100.00 | 0.99–1.03 | 1.41–1.45 | 1.00–1.04 | 1.43–1.46 | 18.5–19.5 | 222.2–250.1 | 0–6 | 0 | True |

## payload4k: 500 original messages/s

| Service | Accepted/planned % | ACK p95 ms | ACK p99 ms | Delivery p95 ms | Delivery p99 ms | CPU % | RAM MiB | Skipped | Rejected | Run integrity |
| --- | --- | --- | --- | --- | --- | --- | --- | --- | --- | --- |
| qgramm | 99.99–100.00 | 2.67–4.05 | 6.33–8.48 | 2.89–4.31 | 6.49–8.66 | 35.4–46.5 | 82.9–89.0 | 0–1 | 0 | True |
| nats | 100.00 | 3.07–3.58 | 7.86–10.92 | 3.39–4.02 | 9.10–13.06 | 18.7–20.6 | 183.3–201.4 | 0 | 0 | True |
| centrifugo | 99.98–100.00 | 1.48–1.59 | 1.82–1.89 | 1.57–1.68 | 1.92–2.01 | 10.7–10.7 | 203.7–204.5 | 0–2 | 0 | False |

## payload4k: 3000 original messages/s

| Service | Accepted/planned % | ACK p95 ms | ACK p99 ms | Delivery p95 ms | Delivery p99 ms | CPU % | RAM MiB | Skipped | Rejected | Run integrity |
| --- | --- | --- | --- | --- | --- | --- | --- | --- | --- | --- |
| qgramm | 65.40–81.06 | 48.59–62.64 | 55.91–74.96 | 50.40–64.68 | 58.02–77.48 | 176.2–184.0 | 110.3–128.4 | 5–9 | 11359–20752 | True |
| nats | 67.52–71.18 | 149.66–160.89 | 169.51–196.73 | 149.87–161.12 | 170.13–197.19 | 36.1–37.0 | 388.4–414.9 | 17292–19486 | 0 | True |
| centrifugo | 99.99 | 1.24–1.28 | 1.91–1.94 | 1.29–1.33 | 1.97–2.00 | 20.8–22.3 | 801.3–806.7 | 5 | 0 | False |

## fanout100: 100 original messages/s

| Service | Accepted/planned % | ACK p95 ms | ACK p99 ms | Delivery p95 ms | Delivery p99 ms | CPU % | RAM MiB | Skipped | Rejected | Run integrity |
| --- | --- | --- | --- | --- | --- | --- | --- | --- | --- | --- |
| qgramm | 100.00 | 4.54–5.32 | 8.46–12.33 | 8.11–8.66 | 13.43–16.09 | 157.9–158.6 | 92.5–92.7 | 0 | 0 | True |
| nats | 90.05–94.00 | 2911.09–3276.27 | 3660.71–3995.66 | 3483.72–3944.20 | 3960.31–4598.04 | 51.2–53.3 | 323.9–340.8 | 120–199 | 0 | True |
| centrifugo | 100.00 | 2.98–3.03 | 3.77–3.84 | 2.96–2.99 | 3.77–3.79 | 14.1–14.2 | 109.2–114.1 | 0 | 0 | True |

## Reconnect

| Service | Accepted originals | Pending at resume | Resume drain ms | Complete / exact |
| --- | --- | --- | --- | --- |
| qgramm | 6000 | 1000 | 105.00–115.30 | True |
| nats | 5989–5990 | 990–991 | 56.29–60.76 | True |
| centrifugo | 6000 | 1000–1001 | 27.35–29.79 | True |

## Idle 10000

| Attempt | Service | Settled samples | CPU % | RAM MiB | Integrity |
| --- | --- | --- | --- | --- | --- |
| idle10000-centrifugo | centrifugo | 5 | 8.1 | 426.2 | True |
| idle10000-nats | nats | 5 | 0.2 | 555.8 | True |
| idle10000-qgramm-retry | qgramm | 5 | 0.6 | 237.2 | True |
| idle10000-qgramm | qgramm | 0 | — | — | False |

## 300-second soak

| Service | Target/s | Planned | Accepted | Skipped | Rejected | Delivery p95 ms | Delivery p99 ms | CPU % | RAM MiB | Integrity | Strict SLO |
| --- | --- | --- | --- | --- | --- | --- | --- | --- | --- | --- | --- |
| centrifugo | 500 | 150000 | 149979 | 21 | 0 | 0.94 | 1.41 | 9.9 | 290.6 | True | False |
| nats | 500 | 150000 | 132301 | 17699 | 0 | 627.35 | 2098.16 | 18.5 | 381.2 | True | False |
| qgramm | 500 | 150000 | 149887 | 51 | 62 | 3.91 | 10.35 | 31.4 | 152.6 | True | False |

## Soak resource windows (first/last 15 samples, approximately 30 seconds)

| Service | Initial mean RAM MiB | Final mean RAM MiB | Initial mean CPU % | Final mean CPU % |
| --- | --- | --- | --- | --- |
| centrifugo | 167.1 | 290.0 | 9.9 | 9.7 |
| nats | 261.5 | 379.8 | 20.4 | 17.3 |
| qgramm | 96.7 | 145.9 | 30.5 | 32.3 |

## NATS FILE stream, consumer state control

| Consumer | Original/s | Accepted/planned % | Delivery p95 ms | Delivery p99 ms | CPU % | RAM MiB | Skipped |
| --- | --- | --- | --- | --- | --- | --- | --- |
| FILE | 10 | 100.00 | 41.65–46.87 | 51.80–54.61 | 21.3–21.4 | 227.4–243.7 | 0 |
| FILE | 50 | 100.00 | 1972.43–2476.26 | 2299.94–2850.75 | 52.3–53.3 | 252.3–278.5 | 0 |
| FILE | 100 | 79.40–79.65 | 5710.89–6094.65 | 6595.38–6859.01 | 49.3–49.3 | 320.7–343.7 | 407–412 |
| FILE | 5 | 95.00–100.00 | 1841.57–4161.51 | 2357.19–5017.79 | 18.8–27.3 | 343.3–352.1 | 0–5 |
| MEMORY | 10 | 100.00 | 5.74–6.81 | 7.63–10.08 | 5.3–6.1 | 187.3–197.9 | 0 |
| MEMORY | 50 | 100.00 | 4.02–4.04 | 5.23–5.42 | 20.5–20.7 | 209.9–217.9 | 0 |
| MEMORY | 100 | 100.00 | 2.58–2.61 | 3.16–3.28 | 33.5–33.6 | 259.3–260.8 | 0 |
| MEMORY | 5 | 100.00 | 6.00–7.96 | 6.86–14.47 | 3.5–3.6 | 260.6–279.3 | 0 |

## QGramm attachments

| Concurrent files | Per-file upload MiB/s | Per-file download MiB/s | Whole flow seconds | Server RAM MiB | Generator RSS MiB | All checks |
| --- | --- | --- | --- | --- | --- | --- |
| 2 × 16 MiB | 45.74–49.17 | 82.14–95.12 | 0.54–0.58 | — | — | True |
| 2 × 64 MiB | 56.26–58.35 | 97.64–100.79 | 1.88–1.92 | 16.7–17.2 | 212.0–212.7 | True |

## Every attempt (raw JSON names)

| Raw path | Superseded | Exit | Integrity | Accepted | Missing | Duplicate | History checked | Error |
| --- | --- | --- | --- | --- | --- | --- | --- | --- |
| corrected/payload4k-centrifugo-r1 | False | 1 | False | 101995 | 0 | 0 | 91020 | history: websocket closed |
| corrected/payload4k-centrifugo-r2 | False | 1 | False | 101993 | 0 | 0 | 90480 | history: websocket closed |
| corrected/payload4k-nats-r1 | False | 0 | True | 81858 | 0 | 0 | 81858 | — |
| corrected/payload4k-nats-r2 | False | 0 | True | 84524 | 0 | 0 | 84524 | — |
| corrected/payload4k-qgramm-r1 | False | 0 | True | 81238 | 0 | 0 | 81238 | — |
| corrected/payload4k-qgramm-r2 | False | 0 | True | 90636 | 0 | 0 | 90636 | — |
| results/consumerstate-file-nats-r1 | False | 0 | True | 2893 | 0 | 0 | 2893 | — |
| results/consumerstate-file-nats-r2 | False | 0 | True | 2883 | 0 | 0 | 2883 | — |
| results/consumerstate-memory-nats-r1 | False | 0 | True | 3300 | 0 | 0 | 3300 | — |
| results/consumerstate-memory-nats-r2 | False | 0 | True | 3300 | 0 | 0 | 3300 | — |
| results/distributed-centrifugo-r1 | False | 0 | True | 101992 | 0 | 0 | 101992 | — |
| results/distributed-centrifugo-r2 | False | 0 | True | 101999 | 0 | 0 | 101999 | — |
| results/distributed-nats-r1 | False | 0 | True | 42598 | 0 | 0 | 42598 | — |
| results/distributed-nats-r2 | False | 0 | True | 39558 | 0 | 0 | 39558 | — |
| results/distributed-qgramm-r1 | False | 0 | True | 99541 | 0 | 0 | 99541 | — |
| results/distributed-qgramm-r2 | False | 0 | True | 97836 | 0 | 0 | 97836 | — |
| results/fanout100-centrifugo-r1 | False | 0 | True | 3300 | 0 | 0 | 3300 | — |
| results/fanout100-centrifugo-r2 | False | 0 | True | 3300 | 0 | 0 | 3300 | — |
| results/fanout100-nats-r1 | False | 0 | True | 3101 | 0 | 0 | 3101 | — |
| results/fanout100-nats-r2 | False | 0 | True | 3180 | 0 | 0 | 3180 | — |
| results/fanout100-qgramm-r1 | False | 0 | True | 3300 | 0 | 0 | 3300 | — |
| results/fanout100-qgramm-r2 | False | 0 | True | 3300 | 0 | 0 | 3300 | — |
| results/files16m-qgramm-r1 | False | 0 | None | None | None | None | None | — |
| results/files16m-qgramm-r2 | False | 0 | None | None | None | None | None | — |
| results/files64m-qgramm-r1 | False | 0 | None | None | None | None | None | — |
| results/files64m-qgramm-r2 | False | 0 | None | None | None | None | None | — |
| results/idle10000-centrifugo | False | 0 | True | 1 | 0 | 0 | 1 | — |
| results/idle10000-nats | False | 0 | True | 1 | 0 | 0 | 1 | — |
| results/idle10000-qgramm-retry | False | 0 | True | 1 | 0 | 0 | 1 | — |
| results/idle10000-qgramm | False | 1 | False | 0 | 0 | 0 | 0 | setup: QGramm connection exhausted retries |
| results/payload4k-centrifugo-r1 | True | 1 | False | 101995 | 0 | 0 | 3060 | history: websocket closed |
| results/payload4k-centrifugo-r2 | True | 1 | False | 101992 | 0 | 0 | 3060 | history: websocket closed |
| results/payload4k-nats-r1 | True | 0 | True | 52514 | 0 | 0 | 52514 | — |
| results/payload4k-nats-r2 | True | 0 | True | 82936 | 0 | 0 | 82936 | — |
| results/payload4k-qgramm-r1 | True | 0 | True | 89245 | 0 | 0 | 89245 | — |
| results/payload4k-qgramm-r2 | True | 0 | True | 89272 | 0 | 0 | 89272 | — |
| results/reconnect-centrifugo-r1 | False | 0 | True | 6000 | 0 | 0 | 6000 | — |
| results/reconnect-centrifugo-r2 | False | 0 | True | 6000 | 0 | 0 | 6000 | — |
| results/reconnect-nats-r1 | False | 0 | True | 5989 | 0 | 0 | 5989 | — |
| results/reconnect-nats-r2 | False | 0 | True | 5990 | 0 | 0 | 5990 | — |
| results/reconnect-qgramm-r1 | False | 0 | True | 6000 | 0 | 0 | 6000 | — |
| results/reconnect-qgramm-r2 | False | 0 | True | 6000 | 0 | 0 | 6000 | — |
| results/soak-centrifugo | False | 0 | True | 149979 | 0 | 0 | 149979 | — |
| results/soak-nats | False | 0 | True | 132301 | 0 | 0 | 132301 | — |
| results/soak-qgramm | False | 0 | True | 149887 | 0 | 0 | 149887 | — |

## Method: QGramm, NATS and Centrifugo

The campaign compares defined application delivery paths on the same machine. It is exploratory performance evidence, not a ranking of equivalent security, persistence or maximum capacity.

The core is unchanged from dev `a16e26b` (core equals `20c3932`); initial tooling is `f35f736`; bounded-history verification and explicitly labelled NATS consumer-state controls are `8eed9fc`. QGramm is built with groups enabled, SQLite WAL/FULL, basic HPKE/storage encryption and full acknowledgements. The file profile additionally compiles files. NATS JetStream 2.15.0 uses FILE storage, one replica, `sync_interval=always`, and a separate durable explicit-ACK consumer per recipient. Centrifugo 6.9.7 uses in-memory bounded history. Official versions: [NATS](https://github.com/nats-io/nats-server/releases/tag/v2.15.0), [Centrifugo](https://github.com/centrifugal/centrifugo/releases/tag/v6.9.7).

QGramm checks Ed25519 tokens/device/chat ACL for commands; connection tickets authorize WebSocket delivery. NATS uses one synthetic shared connection token; Centrifugo uses distinct HS256 connection tokens and permissive benchmark channels. QGramm latency includes sender JWT/HPKE work and recipient decryption; the alternatives carry identical plaintext bytes. NATS sends explicit consumer ACKs after the receive callback; QGramm's fixture does not submit persisted delivery/read receipts. These are useful application paths with different work and trust contracts. Centrifugo memory history cannot survive process restart ([history/recovery documentation](https://centrifugal.dev/docs/server/history_and_recovery)); this campaign does not simulate power loss for any service.

Hardware is Apple M5 Pro,18 logical host CPUs,48 GiB RAM,macOS26.6.2; Docker Desktop Linux6.12.76-linuxkit/aarch64 VM has18 virtual CPUs and8,319,213,568 bytes RAM. Each measured server has4 CPU/8 GiB quotas. The VM actually reports~7.748 GiB effective memory.31 unrelated containers remain running; an initial background sample totaled20.86% of one CPU. Nothing is stopped or retuned. Virtual-disk/SSD/fsync power-loss behavior is not independently qualified. The generator runs natively on the host outside server quotas. Traffic is isolated loopback without TLS; QGramm uses its trusted-proxy benchmark setting. A clean dedicated Linux/SSD reference run is still required.

Every run starts a fresh server/volume. Four comparable workload profiles use2 repeats with reverse service order and15 seconds cooldown:

| Profile | Connections | Conversations | Recipients/conversation | Plaintext | Original messages/s |
|---|---:|---:|---:|---:|---|
| distributed |2,000|1,000|1|256 B|500/1,500/3,000/100,20 seconds each|
| payload4k |2,000|100|1|4 KiB|500/1,500/3,000/100,20 seconds each|
| fanout100 |2,000|10|100|256 B|10/50/100/5,20 seconds each|
| reconnect |1,000|100|1|256 B|200/200/200,10 seconds each;100 recipients offline5 seconds|

Conversations are disjoint, with one sender and the stated recipients; extra connections are idle. Distributed and payload4k differ in both payload and conversation count, so their difference is not a payload-only causal estimate.100 fan-out multiplies deliveries:100 original messages/s means10,000 expected deliveries/s.

The common deadline scheduler is open-loop and limits256 concurrent requests. It records planned,offered,accepted,rejected/backpressure,uncertain and skipped counts. Deadlines more than max(5ms,2 ticks) late are skipped; a full request budget also causes an explicit skip. ACK/delivery latency starts at actual offer before worker dispatch. Scheduler lateness is separate. p50/p95/p99 are nearest-rank successful observations; rejected/skipped work is not silently included as successful low-latency traffic. Every accepted ID must reach every intended recipient with exact payload and ordered live sequences; history checks all accepted IDs and bytes outside workload timing (120-second budget). At-least-once duplicates are counted; integrity allows them, strict load SLO does not.

The strict phase SLO requires complete planned acceptance/delivery,zero rejects/uncertain/skips/duplicates,and delivery p99<=250ms. The highest passing tested phase is not a maximum throughput claim, especially when occasional generator misses disqualify otherwise responsive runs.20-second steps show short-load response. A separate300-second run uses the highest rate passing both distributed repeats, or a labelled exploratory100/s fallback. One soak per service is limited stability evidence. Reconnect deliberately includes offline time in delivery tails; evaluate missing/history and resume drain rather than its normal-delivery SLO. Resume drain begins before new connections are established and ends when deliveries missing at that boundary arrive.

CPU/RAM samples are phase-labelled Docker no-stream samples roughly every2 seconds;100% CPU is one core, RAM is sampled working set, neither is an exact peak. Tables give mean CPU and maximum sampled RAM during the selected phase. Idle values use the last5 idle samples. Generator CPU is `ps` lifetime mean, not instantaneous CPU; generator RSS is separate. Two-repeat min/max ranges are not confidence intervals. Sustained-load history verification and fixture setup are excluded from load latency tables.

Additional checks:10,000 connected users idle20 seconds then one verified message (one run/service), and QGramm-only two concurrent16/64 MiB files (two repeats/size). Files upload half their chunks, verify status, replace the idle HTTP pool, resume missing chunks, retry exact chunks/completion, reject an invalid checksum, deny recipient access before publication,and decrypt/compare every downloaded byte. This is not a server-crash or mid-request disconnect simulation. Client ciphertext buffering contributes to generator RAM. Alternatives do not offer this same attachment workflow, so no invented file-performance comparison is reported.

See raw per-run JSON and image/binary/generator/source provenance. The runner's exit code and campaign exit code are not substitutes for inspecting integrity/load/setup outcomes. Earlier baselines used older competitor versions and a different workload/latency endpoint; do not interpret this campaign as a measured production-core optimization.

The initial 4 KiB Centrifugo runs completed live delivery but failed the final unbounded history request. All six initial payload4k results are preserved as superseded exploratory evidence; the entire profile is repeated in reverse service orders with generator v2 after bounded pagination, rather than selecting only favorable replacements. The additional NATS FILE/MEMORY consumer-state comparison uses two repeats in AB/BA order while retaining FILE stream storage, one replica and always-sync publications. MEMORY consumer state is a within-NATS sensitivity control; only client reconnect while the server stays alive is covered, not state recovery after a server restart.

The paginated 4 KiB repeats also fail the final history gate: Docker reports `OOMKilled=true` for both Centrifugo containers during history validation, despite complete exact live delivery. Preserve these as failed attempts and mark their latency/resource rows live-only. An 8 GiB container quota does not guarantee 8 GiB free memory in the shared ~7.748 GiB VM. This evidence does not identify dedicated-host capacity or prove data loss before termination.

The 16 MiB concurrent file flows finish in under one resource-sampling interval; no RAM/CPU sample is available and the table shows a dash, not zero usage. The 64 MiB flows provide very few samples, so their RAM numbers are observations, not reliable peaks. The initial QGramm idle10k attempt fails during connection setup, before idle measurement; it has no valid idle RAM value. An explicitly named later retry is retained separately and does not erase the first attempt.
