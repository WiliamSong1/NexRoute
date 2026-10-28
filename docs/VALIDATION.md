# Validation record

Validated locally on October 27, 2026 (America/Los_Angeles), on Apple M3 / macOS arm64, using Go 1.27.3, k6 1.0.0, Docker Engine 29.9.2, and Docker Compose v5.6.0.

## Verified behavior

- `go test -race -count=1 -coverpkg=./internal/... ./...` passed on the final Go source.
- `go vet ./...` and `gofmt` checks passed.
- Five Python harness/analysis tests passed.
- Native executables built successfully; Docker built Linux executables with the pinned Go builder.
- Compose started three simulators, the load balancer, Prometheus, and Grafana.
- The provisioned 12-panel Grafana dashboard and Prometheus data-source health were verified through their APIs; every dashboard PromQL expression executed successfully.
- Live container requests preserved query parameters. A forced backend outage removed A from active health without making the pool unready; restoration returned A to eligible health.
- Graceful shutdown completed a 500 ms in-flight request before the load balancer exited with code 0.
- The provided reusable integration script exercises the above container checks; it stops the load balancer after verifying shutdown.

Coverage is recorded in [coverage.out](validation/coverage.out). The aggregate internal-package statement coverage was approximately 80%; this is not a correctness guarantee. Commands are exercised by the real native and container runs rather than unit tests of `main`.

## Measured exploratory runs

Eight homogeneous-request/heterogeneous-backend runs compared all four policies twice, each with two seconds warmup and six seconds measurement at 300 offered requests/s. Means of the two run summaries:

| Algorithm | Completed RPS | P95 ms | P99 ms | Error fraction | Dropped |
| --- | ---: | ---: | ---: | ---: | ---: |
| adaptive | 297.46 | 45.81 | 67.13 | 0.0000 | 0 |
| least_connections | 298.25 | 64.27 | 70.16 | 0.0000 | 0 |
| round_robin | 297.13 | 68.83 | 70.76 | 0.0000 | 0 |
| weighted_round_robin | 296.99 | 69.46 | 70.66 | 0.0000 | 0 |

These differences are exploratory. Two runs are far too few for a reliable general performance claim. The chart's confidence bars describe only the sampled run means. Completed RPS includes failures; none occurred in this particular matrix.

[Raw steady-workload samples and report](../benchmarks/sample-results/smoke/REPORT.md) · [Chart](../benchmarks/sample-results/smoke/comparison.png) · [Paired differences](../benchmarks/sample-results/smoke/paired_comparisons.csv)

Sixteen further runs compared all four algorithms once across latency spikes, outages, application errors, and traffic spikes. Each used two seconds warmup and nine seconds measurement at a base rate of 900 requests/s (up to 2,700 during the traffic ramp). All completed with no dropped k6 arrivals. Several runs had HTTP failures; those are retained in the results and must be considered alongside latency. P99 did not favor adaptive over every baseline in every workload.

[Raw dynamic samples and report](../benchmarks/sample-results/dynamic-smoke/REPORT.md) · [Per-run metrics](../benchmarks/sample-results/dynamic-smoke/runs.csv) · [Chart](../benchmarks/sample-results/dynamic-smoke/comparison.png)

These are 24 actual k6 experiments, not fabricated results or Go microbenchmarks relabeled as HTTP throughput. The larger default matrix is provided but was not run in this session. No statistical superiority claim is made. All components shared one laptop; repeat on dedicated hosts with longer measurements and independent replications before publication.

The recorded k6 runs exercised GET requests. Subsequent review corrected the non-replayable upload timer, tightened metric-label ID validation, and made readiness permit recovery probes after circuit cooldown; the final source passed the expanded race tests and live container checks. An additional three-second k6 check of the final adaptive build is saved in `benchmarks/sample-results/final-check`, with its exact source digest. Re-run the matrix on the final source before treating these samples as a release baseline. Dynamic-run metadata retains the source digest of the measured version.

## Microbenchmarks

[Raw routing and reservation results](validation/microbenchmarks.txt) include allocations and runtime platform information. The three-candidate policy-only adaptive benchmark measured about 57 ns/op with no allocations; the one-backend concurrent pool reservation benchmark measured about 435 ns/op and two allocations per operation. These are deliberately small synthetic cases, not a claim of end-to-end proxy latency or large-pool scalability. Idle observability containers were running during this microbenchmark pass.

## Scope and remaining validation

The complete source, deployment, dashboards, tests, analysis scripts, and measured samples are included. The original pasted brief ended mid-sentence after the benchmarking metric "Error"; no additional unseen requirements were assumed.

Not established by this session: long-duration soak behavior, multi-host scaling, production TLS/auth integration, Internet-facing hardening, large backend-pool performance, or a statistically robust improvement over every baseline. GitHub Actions is configured but has not been executed on a remote repository. See [design boundaries](DESIGN.md) and [experimental protocol](BENCHMARKS.md).
