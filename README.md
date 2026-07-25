# slo-calc

[![ci](https://github.com/moveeeax/slo-calc/actions/workflows/ci.yml/badge.svg)](https://github.com/moveeeax/slo-calc/actions/workflows/ci.yml)
[![Go](https://img.shields.io/badge/go-1.22%2B-00ADD8?logo=go)](https://go.dev/)
[![License: MIT](https://img.shields.io/badge/license-MIT-blue.svg)](LICENSE)

Turn raw Prometheus SLIs into **SLOs, error budgets, burn rates**, and ready-to-apply
**multi-window multi-burn-rate alerting rules** — from a single YAML spec.

`slo-calc` does the error-budget arithmetic from the [Google SRE workbook](https://sre.google/workbook/alerting-on-slos/)
so you don't have to hand-derive burn-rate thresholds for every service.

## What it does

- **Reads SLI queries from a YAML spec** — ratio *or* latency SLIs.
- **Computes** availability, error budget remaining (as a share *and* as wall-clock
  minutes), and burn rate over any window.
- **Generates multi-burn-rate alert rules** (page + ticket, long + short window) as a
  promtool-valid Prometheus rules file.
- **Outputs** a human table, JSON for dashboards, or the rules file.
- **Backfills** — evaluate at an arbitrary past instant with `--at`.

## Install

```shell
go install github.com/moveeeax/slo-calc@latest
```

or build from source:

```shell
git clone https://github.com/moveeeax/slo-calc && cd slo-calc
go build -o slo-calc .
```

## The spec

A spec is a list of SLOs. Each SLO has an `objective` (target %), a `type`
(`ratio` or `latency`), and `good`/`total` PromQL expressions. The literal
`$window` is substituted with the rate window being evaluated — the SLO window
for the report, and each burn-rate window (`5m`, `30m`, `1h`, `2h`, `6h`, `1d`,
`3d`) when generating rules.

```yaml
slos:
  - name: api-availability
    objective: 99.9
    type: ratio
    good:  sum(increase(http_requests_total{job="api",code!~"5.."}[$window]))
    total: sum(increase(http_requests_total{job="api"}[$window]))
    labels: { team: platform }

  - name: api-latency
    objective: 99.0
    type: latency
    good:  sum(increase(http_request_duration_seconds_bucket{job="api",le="0.3"}[$window]))
    total: sum(increase(http_request_duration_seconds_count{job="api"}[$window]))
```

See [`examples/slos.yaml`](examples/slos.yaml).

## Usage

Report the current budget state (queries Prometheus):

```shell
slo-calc --spec examples/slos.yaml --window 30d --prometheus http://localhost:9090
```

```
SLO               OBJECTIVE  AVAIL     ERR BUDGET  BUDGET LEFT       BURN   STATUS
api-availability  99.900%    99.9820%  43.2m       35.4m (82.0%)     0.18x  OK
api-latency       99.000%    98.7500%  7.2h        -108.0m (-25.0%)  1.25x  BREACHED
```

`ERR BUDGET` is the whole budget the objective buys you over the window in
wall-clock terms; `BUDGET LEFT` is what is still unspent. A negative value means
the objective has already been missed over the window.

JSON for dashboards:

```shell
slo-calc --spec examples/slos.yaml --prometheus http://localhost:9090 --output json
```

Generate Prometheus alerting rules (no Prometheus needed — pure spec → rules):

```shell
slo-calc --spec examples/slos.yaml --window 30d --output rules > slo-rules.yaml
promtool check rules slo-rules.yaml   # SUCCESS: 18 rules found
```

A ready-made example is committed at [`examples/rules.yaml`](examples/rules.yaml).

Backfill — evaluate the budget as it stood at a past instant:

```shell
slo-calc --spec examples/slos.yaml --prometheus http://localhost:9090 \
  --at 2026-06-01T00:00:00Z
```

### Flags

| Flag | Default | Description |
|------|---------|-------------|
| `--spec` | *(required)* | Path to the SLO spec YAML |
| `--window` | `30d` | SLO window (`30d`, `7d`, `24h`, `1w`, …); must be positive |
| `--output` | `table` | `table`, `json`, or `rules` |
| `--prometheus` | — | Prometheus base URL (required for `table`/`json`) |
| `--at` | now | RFC3339 instant to evaluate at (backfill) |
| `--version` | | Print version and exit |

## How the maths works

For each SLO over the window:

```
availability   = good / total
error budget   = 1 - objective            # e.g. 99.9% -> 0.001
consumed       = (1 - availability) / error budget
burn rate      = (1 - availability) / error budget
budget minutes = window in minutes * error budget
```

`budget minutes` is the familiar "how long may I be down this month" figure. It
assumes a uniform request rate across the window; with bursty traffic the
event-ratio budget above is the authoritative one.

| Objective | 7d      | 28d     | 30d     | 90d      | 365d     |
|-----------|---------|---------|---------|----------|----------|
| 99%       | 100.8m  | 403.2m  | 432m    | 1296m    | 5256m    |
| 99.9%     | 10.08m  | 40.32m  | 43.2m   | 129.6m   | 525.6m   |
| 99.95%    | 5.04m   | 20.16m  | 21.6m   | 64.8m    | 262.8m   |
| 99.99%    | 1.008m  | 4.032m  | 4.32m   | 12.96m   | 52.56m   |

A **burn rate of 1** spends the entire budget exactly at the end of the window.
The generated alert rules fire when a **long** and a **short** window *both*
exceed a burn-rate threshold, following the standard table (at a 30-day window):

| Severity | Long | Short | Burn rate | Budget consumed |
|----------|------|-------|-----------|-----------------|
| page     | 1h   | 5m    | 14.4×     | 2%              |
| page     | 6h   | 30m   | 6×        | 5%              |
| ticket   | 1d   | 2h    | 3×        | 10%             |
| ticket   | 3d   | 6h    | 1×        | 10%             |

The factors scale automatically for other SLO windows
(`burn = consumed × sloWindow / longWindow`).

### A caveat on loose objectives

The alert threshold is `burn rate × error budget`, and it is compared against an
error *ratio*, which can never exceed 1. Below roughly a 93% objective the 14.4×
page row needs a ratio above 1 — the rule is emitted, `promtool` accepts it, and
it can never fire. `slo-calc` warns on stderr when this happens:

```
slo-calc: warning: slo "loose" (90%): the page row 1h/5m needs an error ratio
above 1.44, but a ratio cannot exceed 1 — this alert can never fire.
Tighten the objective or lengthen the SLO window.
```

The JSON output carries the same information as a `"reachable"` field on each
alert.

## Development

```shell
go test ./...      # unit tests (exact error-budget maths, rule validity, client)
go vet ./...
```

## License

[MIT](LICENSE)
