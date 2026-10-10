# Contributing to NetGold Monitor

Thank you for helping make free network monitoring better for everyone.
NetGold Monitor is a single-file Go program with an embedded web UI —
contributions are welcome, from bug reports to new monitors.

## Getting started

You need Go 1.22 or newer. There are no other build dependencies: the
program is pure Go (`CGO_ENABLED=0`) and embeds everything it serves.

```bash
git clone <this repository>
cd netgold
go vet ./... && go test ./...
go build -o netgold ./cmd/netgold
./netgold -demo        # dashboard on http://localhost:8080 with a demo device
```

## Project layout

| Directory | What lives there |
|---|---|
| `cmd/netgold` | Program entry point (flags, wiring) |
| `internal/store` | SQLite persistence (devices, events, maps, topologies, settings) |
| `internal/poller` | Polling engine: ping, SNMP v2c/v3, bandwidth, service checks |
| `internal/discovery` | Subnet auto-discovery (ping sweep + SNMP probe) |
| `internal/legacyimport` | legacy database importer (live SQL Server, auto-detect, .bak) |
| `internal/report` | Downtime calculation + Excel/PDF reports |
| `internal/topoxl` | Topology Excel template / import / export |
| `internal/notify` | Email alerts (SMTP) |
| `internal/web` | Embedded dashboard (HTML/CSS/JS, no build step) and the JSON API |

## Ground rules

- **Tests must pass.** Run `go vet ./... && go test ./...` before opening
  a pull request. New behaviour should come with tests in the same style
  as the existing ones (small, deterministic, no real network gear —
  local test servers and temporary databases).
- **Proper English only** in code, comments, UI text and docs.
- **No external runtime dependencies for the UI.** The dashboard must
  keep working fully offline: no CDNs, no web fonts, no trackers.
  JavaScript is vanilla; CSS is hand-written.
- **Keep the single-file promise.** New Go dependencies need a good
  reason; the binary must stay self-contained (`CGO_ENABLED=0`).
- **Never log or print credentials.** SNMP communities, SNMP v3
  passwords and the SMTP password live only in the local database and
  are masked in API responses.
- Match the existing code style (`gofmt`).

## Reporting bugs

Open an issue with: your platform, how you run NetGold (flags), what you
expected, what happened, and — for polling problems — the device type and
which monitors are configured. Please do not include real credentials or
public IP addresses of your production network.

## Feature ideas

Check `ROADMAP.md` first — the larger features (NetFlow, syslog, SNMP
traps, LLDP topology, Windows Service mode) are already planned in
phases. Small, focused pull requests land fastest.
