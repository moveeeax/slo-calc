# slo-calc

> Turn raw SLIs into SLOs, error budgets, and burn rates.

**Status:** 🚧 In development

## Overview

CLI to compute SLOs and error budgets from Prometheus SLIs.

## Features

- Reads SLI queries from a YAML spec
- Computes availability, error budget remaining, burn rate
- Multi-window multi-burn-rate alert thresholds
- Outputs table/JSON and Prometheus rules
- Backfill over an arbitrary time range

## Stack

Go + Prometheus HTTP API.

## Usage

```bash
slo-calc --spec slos.yaml --window 30d
```

## License

MIT
