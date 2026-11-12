# slo-calc

[![ci](https://github.com/moveeeax/slo-calc/actions/workflows/ci.yml/badge.svg)](https://github.com/moveeeax/slo-calc/actions/workflows/ci.yml)
[![Go](https://img.shields.io/badge/go-1.26%2B-00ADD8?logo=go)](https://go.dev/)
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
- **Measures SLIs from access logs** (`slo-calc logs`) when a service has no
  request metrics yet, and emits the spec and rules to keep measuring them.
- **Sends a burn digest to Slack or Telegram** (`slo-calc digest`), quiet while
  budgets are healthy, with a non-zero exit for release gates.

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

## SLIs from access logs

`slo-calc logs` reads access logs and computes availability (share of non-5xx
responses) and, with `--latency-target`, the share of requests at or under the
target, plus latency percentiles, per route and method. Use it to get a first
number for a service that has no request metrics yet.

```shell
slo-calc logs --input /var/log/nginx/access.log --format combined \
  --objective 99.9 --latency-target 300ms \
  --exclude-path '^/healthz' --exclude-cancelled
```

```
analysed 6 requests from 9 lines (2 excluded, 1 unparsable), 2026-11-01T00:00:00Z to 2026-11-01T01:00:00Z (1h)

SLI               OBJECTIVE  ACTUAL    BURN     30d BUDGET SPENT  STATUS
availability      99.900%    66.6667%  333.33x  46.3%             BURNING
latency <= 300ms  99.900%    66.6667%  333.33x  46.3%             BURNING

latency p50 80ms  p90 950ms  p99 950ms (6 timed requests)

worst routes (4 of 4)
METHOD  ROUTE                 REQUESTS  5XX  5XX SHARE  SLOW  SLOW SHARE  P50    P90    P99
POST    /orders               2         1    50.0%      2     100.0%      410ms  950ms  950ms
GET     /users/:uuid/profile  1         1    50.0%      0     0.0%        100ms  100ms  100ms
GET     /orders/:id           2         0    0.0%       0     0.0%        12ms   20ms   20ms
GET     /search               1         0    0.0%       0     0.0%        80ms   80ms   80ms
```

`BURN` is the burn rate over the span the logs cover. `30d BUDGET SPENT` is the
share of a full window's budget that span used up (`burn × span / window`),
which assumes the logged traffic is typical for the window. `5XX SHARE` and
`SLOW SHARE` are each route's part of the failed and slow requests, i.e. of the
budget spent.

Health checks, crawlers and probes distort the number; drop them with
`--exclude-path '^/(healthz|readyz|metrics)'` and
`--exclude-agent '(?i)bot|crawler|spider|kube-probe'` (both repeatable).

Formats:

| `--format` | Layout | Duration |
|------------|--------|----------|
| `combined` | nginx / Apache combined | optional last field: `$request_time` or `rt=$request_time` (seconds); Apache `%D` with `--duration-unit us` |
| `common`   | Common Log Format | same optional last field |
| `envoy`    | Envoy's default access log format | `%DURATION%` (milliseconds) |
| `json`     | one JSON object per line | field `duration`, seconds unless `--duration-unit` says otherwise |

JSON keys default to `time`, `method`, `path`, `status`, `duration`,
`user_agent` and `request` (a `GET /x HTTP/1.1` line, used when `method` or
`path` is missing). Remap them with `--field status=resp.code`; dots walk into
nested objects. `time` may be RFC3339, nginx `$time_local` or a Unix timestamp.
`.gz` inputs are decompressed and `--input -` reads stdin.

A response counts as failed when its status is 5xx, or 0 in Envoy (no response
sent) unless the client hung up. Client-cancelled requests (nginx 499, Envoy
`DC`) are not failures; `--exclude-cancelled` also keeps their duration out of
the latency SLI.

Paths become route templates: the query string is dropped and numeric, UUID,
long hex and opaque id segments turn into `:id`, `:uuid` and `:hash`, so
`/orders/8213` and `/orders/8214` count as `/orders/:id`. Name routes the rules
miss with `--route /repos/:owner/:repo` (repeatable; `*` matches one segment
too).

To keep measuring once the service exposes metrics, write the matching spec
and rules:

```shell
slo-calc logs --input access.log --objective 99.9 --latency-target 300ms \
  --name shop --selector 'job="shop"' \
  --emit-spec shop-slos.yaml --emit-rules shop-rules.yaml
```

The spec uses promhttp's names (`http_requests_total` with a `code` label,
`http_request_duration_seconds` histogram), changeable with
`--metric-requests`, `--code-label` and `--metric-duration`. The latency SLO
reads the bucket `le="<target in seconds>"`, so the histogram needs a bucket at
exactly that boundary. The rules file is the same as running
`slo-calc --spec shop-slos.yaml --output rules` on the emitted spec.

| Flag | Default | Description |
|------|---------|-------------|
| `--input` | *(required)* | Log file, `-` for stdin, `.gz` decompressed (repeatable) |
| `--format` | `combined` | `combined`, `common`, `envoy` or `json` |
| `--duration-unit` | `s` (`ms` for envoy) | Unit of a numeric duration: `s`, `ms`, `us`, `ns` |
| `--field` | | JSON key mapping `name=key` (repeatable) |
| `--objective` | `99.9` | Availability objective, percent |
| `--latency-target` | off | Latency threshold (`300ms`); enables the latency SLI |
| `--latency-objective` | `--objective` | Latency objective, percent |
| `--window` | `30d` | SLO window for the budget-spent column and the rules |
| `--route` | | Route template (repeatable) |
| `--exclude-path` | | Regexp on the raw path to drop (repeatable) |
| `--exclude-agent` | | Regexp on the user agent to drop (repeatable) |
| `--exclude-cancelled` | `false` | Drop nginx 499 / Envoy `DC` requests |
| `--top` | `10` | Worst routes to show, `0` for all |
| `--output` | `table` | `table` or `json` |
| `--emit-spec` | | Write an slo-calc spec to this file |
| `--emit-rules` | | Write recording and burn-rate alerting rules to this file |
| `--name` | `service` | SLO name prefix in the emitted spec |
| `--selector` | | Label matchers added to every emitted series |
| `--metric-requests`, `--code-label`, `--metric-duration` | promhttp names | Metric names for the emitted spec |

All durations are held in memory for the percentiles, roughly 8 bytes per
request.

## Burn digest for Slack and Telegram

`slo-calc digest` evaluates the spec once and sends a short message: a headline
plus one line per SLO that tripped a fast-burn row, ran out of budget, or moved,
with the per-SLO detail in a thread. Run it on a schedule (cron, a Kubernetes
CronJob, a CI schedule); it has no scheduler of its own.

```shell
export TELEGRAM_BOT_TOKEN=...
slo-calc digest --spec slos.yaml --prometheus http://prometheus:9090 \
  --deliver telegram --chat-id "$SLO_CHAT_ID"
```

```
SLO digest at 2026-11-12 09:00 UTC, 30d window, change over the last 1d
[FAST BURN] api-availability: budget left 50.0% (no change over 1d), 1h/5m burn 20.0x/20.0x over 14.4x
1 other SLO healthy and unchanged
```

Thread reply:

```
api-availability (objective 99.9%)
  availability 99.9500% over 30d, budget left 50.0% (21.6m of 43.2m), burn 0.50x
  1d ago: budget left 50.0%, now 50.0% (no change)
  burn rate 5m 20.00x, 30m 20.00x, 1h 20.00x, 6h 0.50x
  page 1h/5m over 14.4x: TRIPPED
  page 6h/30m over 6x: not tripped
...
```

- Fast burn uses the two page rows of the table above (1h/5m and 6h/30m at a
  30d window, scaled for other windows). A row trips when both windows are over
  the threshold, the same condition as the generated `SLOBurnPage` alert.
- "Moved" compares budget left now with `--compare` ago (default `1d`); a
  change of `--moved` percentage points of the budget or more (default `2`,
  the smallest row of the table) names the SLO.
- When every budget is healthy, nothing tripped and nothing moved, nothing is
  sent (`--always` sends anyway).
- In Telegram only a digest with a tripped fast-burn row notifies; other
  digests and the replies go out silently (`disable_notification`).
- `--fail-on-burn` exits with status 2 when a fast-burn row is tripped or a
  budget is exhausted, after delivering, so a release pipeline can refuse to
  ship. Other failures exit with 1.

Delivery:

| `--deliver` | Needs | Detail |
|-------------|-------|--------|
| `stdout` | nothing | printed after the summary |
| `slack` | `SLACK_WEBHOOK_URL` | incoming webhooks cannot thread; detail follows as separate messages |
| `slack` | `SLACK_BOT_TOKEN` and `--slack-channel C0123` | `chat.postMessage`, detail in the summary's thread (bot needs `chat:write`) |
| `telegram` | `TELEGRAM_BOT_TOKEN` and `--chat-id` or `TELEGRAM_CHAT_ID` | Bot API `sendMessage`, detail as replies |

Tokens are read only from the environment, never from flags, so they stay out
of `ps` output and shell history. Messages are plain text and split on line
boundaries to fit Telegram's 4096-character and Slack's recommended
4000-character limit.

| Flag | Default | Description |
|------|---------|-------------|
| `--spec` | *(required)* | SLO spec YAML |
| `--prometheus` | *(required)* | Prometheus base URL |
| `--window` | `30d` | SLO window |
| `--at` | now | RFC3339 instant to evaluate at |
| `--compare` | `1d` | How far back to compare budget left; `0` turns it off |
| `--moved` | `2` | Percentage points of budget that count as moved |
| `--deliver` | `stdout` | `stdout`, `slack` or `telegram` |
| `--slack-channel` | | Channel ID for bot-token delivery |
| `--chat-id` | `$TELEGRAM_CHAT_ID` | Telegram chat ID |
| `--always` | `false` | Deliver even when quiet |
| `--fail-on-burn` | `false` | Exit 2 on fast burn or an exhausted budget |

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
