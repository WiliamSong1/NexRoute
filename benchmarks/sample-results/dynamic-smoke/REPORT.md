# Benchmark interpretation

16 completed runs. Dots are independent run summaries; bars are 95% bootstrap intervals for their means.

Quantiles are computed per run; these are means of run quantiles, not pooled latency quantiles. Comparisons pair the same repetition and scenario. Lower latency is only useful alongside error rate and dropped arrivals. RPS includes failed HTTP requests. Failed runs must be investigated, not removed.

**Exploratory only:** fewer than five independent repetitions in at least one group. Do not claim statistical superiority from this smoke run.

Bootstrap intervals describe observed run-to-run variation, not host/environment uncertainty. Multiple comparisons are not corrected; use a predeclared primary endpoint and independent confirmation before publishing a superiority claim.
