# Experimental protocol

## Questions and endpoints

Predeclare the primary question before running a large experiment, for example: "At 900 offered requests/s during an A-backend latency spike, does adaptive routing reduce run P99 relative to round robin without increasing error fraction or dropped arrivals?" P95/P99, errors, successful throughput, and dropped work must be considered together. A router can achieve apparently low latency by failing fast.

The default matrix compares all four policies over five workloads with five independent process restarts per workload/policy. Ten seconds of warmup precede thirty seconds of measurement. A full matrix takes roughly 70–90 minutes including readiness and process startup; the exact duration depends on host and teardown. Increase repetitions and measurement durations for publication.

## Workloads

| Workload | Behavior |
| --- | --- |
| heterogeneous | A: 10±2 ms, capacity 8; B: 30±5 ms, capacity 32; C: 60±10 ms, capacity 64 |
| latency_spike | Add 400 ms to A during the middle third |
| outage | A rejects data/health requests during the middle third |
| errors | A returns 500 with probability 0.4 during the middle third |
| traffic_spike | Offered rate ramps to 3× base during the middle third and back in the final third |

Proxy and simulator capacity limits match for every algorithm. Weights are 1:2:3. All use identical health checks, retry settings, timeouts, and saturation admission. The workload uses homogeneous GETs so differences can be interpreted without a request-class confounder. Add upload/stream, CPU, variable-size, and mixed-route experiments before extrapolating to production traffic. CPU work is available through the simulator control endpoint but is not part of the default matrix.

The runner uses fresh services per experiment, matched backend seeds per repetition, and shuffled algorithm/scenario order within repetition blocks. Faults run on the controller clock relative to k6 process launch; k6 startup introduces a small timing offset. Longer phases reduce its impact. A stricter publication harness could use an explicit generator/controller barrier.

## Arrival model and load generator validity

k6 uses a constant-arrival-rate executor for the first four scenarios, and ramping-arrival-rate for traffic spikes. It does not wait for one response to schedule the next arrival, reducing coordinated-omission bias. A finite VU ceiling remains: dropped_iterations is essential evidence of generator or system saturation. Default maximum is 1,000 VUs, with 200 preallocated.

Run the load generator on a separate, adequately provisioned machine for final performance numbers. The included native harness intentionally runs all processes on one host for convenience and repeatability, so CPU contention and shared loopback networking limit conclusions. Docker Compose is an observability/demo environment; the native harness is the controlled comparison path. Avoid running both simultaneously.

The optional `--algorithms direct` targets only backend A. It is a single-backend capacity reference, not a fair comparison against the aggregate pool or an isolated measurement of proxy overhead. To estimate overhead, compare a single-backend proxy configuration against that same direct target at matched low offered load on separate runs.

## Recorded artifacts

Each run directory contains:

- `metadata.json`: workload, algorithm, repetition, randomized order, settings, seed, versions, platform, and completion status. Newer harness runs also record a source/config digest and full proxy configuration.
- `summary.json`: raw k6 metric statistics, including latency quantiles and request failure rate.
- `warmup.json`: separate warmup summary, excluded from analysis.
- `metrics-before.prom` / `metrics-after.prom`: backend attempts, errors, retries, transitions, and other state around the measured phase.
- Logs from the proxy, each backend, and both k6 phases.

A failing service, failed fault injection, generator timeout, or failed k6 execution marks the experiment incomplete and stops the matrix. Existing runs are never overwritten. Investigate failures instead of deleting them to improve a chart. The analysis refuses incomplete runs.

## Analysis and statistical limits

`analyze.py` reports per-run RPS, successful RPS, P50/P95/P99, mean latency, error fraction, and dropped iterations. RPS counts HTTP attempts completed by k6, including failures. Successful RPS is completed RPS multiplied by one minus the k6 failure fraction.

For each metric and group it computes the mean of independent run summaries with a seeded 10,000-resample percentile bootstrap 95% interval. It never pools quantiles or averages raw request samples across runs and labels the plotted mean-of-run-quantiles accordingly. One run has no estimable interval. With only two or three runs, bootstrap intervals are unstable and exploratory.

Adaptive/baseline comparisons use matched repetition IDs within each scenario and bootstrap their paired differences. Negative P99/P95/error differences favor adaptive; positive RPS differences favor adaptive only if error/drop behavior is comparable. Intervals describe observed within-host variation, not systematic host bias. Multiple comparisons are not corrected. Publish a predeclared primary endpoint and independent confirmation, or apply an appropriate multiplicity adjustment before making broad claims.

The included short runs verify the pipeline and expose workload tradeoffs. They do not establish that adaptive routing wins in every workload. Re-run at several rates (below saturation, near saturation, overload), with longer fault/recovery windows, and collect CPU/memory profiles before any optimization claims.

## Profiling

```sh
go test -run '^$' -bench . -benchmem ./internal/routing ./internal/pool
go test -run '^$' -bench BenchmarkPoolReservation -cpuprofile cpu.prof ./internal/pool
go tool pprof cpu.prof
```

Routing benchmarks isolate scoring/policy work; pool benchmarks include locking, reservations, allocations, and observation updates. Neither is an end-to-end RPS result. Prometheus/Grafana provide process CPU and Go heap during Compose workloads. Use OS/container tools for RSS, socket counts, and generator saturation; the application does not pretend heap is total process memory.
