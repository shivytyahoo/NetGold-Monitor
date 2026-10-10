# NetGold Monitor — Roadmap

Status key: ✅ done · 🔨 in progress · 🗓️ planned · ❌ out of scope

## Monitoring core
- ✅ Ping monitoring (latency, packet loss), 60 s polling (the legacy system defaults: 1 s timeout, 1 retry, 32-byte payload; interval bounds 10 s – 24 h)
- ✅ SNMP v2c polling — uptime, CPU, memory, all interfaces
- ✅ SNMP v3 (USM) — authNoPriv and authPriv (MD5/SHA, DES/AES)
- ✅ MRTG/Cacti-style graphs — bandwidth in/out (64-bit counters), latency, CPU, memory; 1 h – 30 d
- ✅ Service monitors executed by the poller with the legacy system Send/Expect semantics: HTTP, HTTPS, FTP, SMTP, POP3, IMAP, NNTP, Telnet, SSH, DNS, Echo, Time, generic TCP — per-monitor interval/timeout/retries
- ✅ Device classification by sysObjectID enterprise prefix (subset of the legacy system's DeviceType table)
- 🗓️ SNMP traps receiver (passive monitors)
- 🗓️ WMI / Windows service + performance monitoring
- ✅ NetFlow v5 / v9 / IPFIX + sFlow v5 collector with top-talkers reports (v2.1) — 1-minute aggregates + live flows view; decoder fixtures and live datagram tests passed with exact byte totals
- ✅ Syslog collection and search (v2.1) — RFC 3164 + RFC 5424, UDP + TCP; Windows Event Log collection still planned

## Discovery & mapping
- ✅ Subnet auto-discovery (ping sweep + SNMP probe)
- ✅ legacy database import — one-click auto-detect, .bak file, manual connection
- ✅ Backup (.bak) import hardening (v2.2) — instance ports (static + dynamic) resolved from the Windows registry (works with the SQL Browser service stopped), precise situation errors (none found / not answering / sign-in failed) with per-instance details, manual server override in the Backup File card
- ✅ Automatic SQL engine setup (v2.2, Windows) — when no SQL Server is usable, NetGold downloads Microsoft's SQL Server 2022 LocalDB (verified URL, ~64 MB), enforces a Valid Microsoft Authenticode signature before running anything, installs quietly, creates a dedicated instance and imports automatically; refuses under the Windows service with desktop-run guidance
- ✅ Engine-setup offer hotfix (v2.2.1) — the Backup File card shows the engine status line and the setup panel proactively on page open (not only after a failed import), all backup-import failures carry the structured error shape, and the embedded UI is served with Cache-Control: no-store so an upgraded program can never be paired with a stale cached interface
- ✅ Maps from the legacy database — same device positions
- ✅ Topology segment — create from an imported map, from Excel import (template provided; auto-layout), or manually in the built-in editor (drag, link, save)
- ✅ Topology Excel export (round-trippable) + live device status on nodes
- ✅ LLDP/CDP neighbour discovery → automatic real links between devices (v2.1) — Layer-2 connectivity phase: ARP + bridge FDB (dot1d + dot1q) + LLDP/CDP correlation, interface MACs stored from ifPhysAddress; correlation verified with fixture tests (LLDP merge, CDP-only, FDB single-device, ARP-only = no link)
- 🗓️ Role templates for discovery (automatic monitor sets per device role)

## Dashboard & UI
- ✅ Modern dark NOC design: sidebar navigation, status cards, live top bar, glass cards, motion system (view transitions, count-up counters, breathing status glows, slide-in toasts, skeleton loaders), `prefers-reduced-motion` respected
- ✅ Pages: Dashboard, Devices, Device Detail, Maps, Topology (incl. Discovered Links), Traffic, Logs, Reports, Alarms, Discovery, Import, Settings, About
- ✅ Sound alarm system: three WebAudio sound packs (Classic Siren, Soft Chime, Digital Beeps), volume control, subtle UI feedback sounds, desktop notifications, acknowledge
- ✅ Map animation (flowing lines, down-device pulse, one-time ring on new outages, 15 s live refresh)
- ✅ Down escalation labels (Down, Down at least 2 min / 5 min / 20 min), as in the legacy system

## Alerting & reporting
- ✅ Downtime calculation per link/device — outages, totals, uptime %
- ✅ Excel + PDF downtime reports (all links, any period)
- ✅ Email alerts (SMTP, STARTTLS/TLS) on down/up with downtime duration
- 🗓️ Maintenance windows — downtime precedence **Maintenance > Down > Up** (Unknown ignored), exactly as the legacy system's reporting resolves overlapping states; maintenance periods never count as downtime
- 🗓️ Scheduled reports (daily/weekly email), alert escalation
- 🗓️ Action policies (run a script/webhook on state change)

## Platform & operations
- ✅ Single-file binaries for Windows, Linux, macOS (Intel and Apple Silicon) — no runtime, embedded SQLite, zero setup
- ✅ Cross-platform (Go, `CGO_ENABLED=0`)
- ✅ Windows Service mode (v2.1) — NetGoldMonitor service with `-service install|uninstall|start|stop|status`, automatic start and recovery restarts; sample systemd unit for Linux. Compiled and vetted for Windows; live service-control test on a Windows machine still pending. Installer package still planned
- ✅ Self-replacing single instance (v2.2.2) — a newly started copy replaces an older running one (version probe on the listen port, process sweep; the NetGoldMonitor service is reported, never killed; `-no-takeover` opts out), on Windows the program installs itself to %LOCALAPPDATA%\NetGold and runs from there, the data directory defaults to a fixed per-user location with a one-time carry-over of a legacy folder, and the dashboard banners expose a stale tab or a closed program instead of showing a dead page
- 🗓️ Multi-user roles (admin/viewer), audit log
- 🗓️ Backup/restore of NetGold's own database from the UI

## Out of scope
- ❌ AI threat detection / threat hunting / packet forensics — these are
  separate paid security add-ons in the legacy system world, not network
  monitoring; NetGold stays a monitoring tool.

---

NetGold Monitor is developed by **Shivam Bhalla** and released under the
MIT License — free and open source, for every IT professional.
