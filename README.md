# Codex Quota Scheduler

## v0.4.0 account eligibility

v0.4.0 filters disabled, unavailable, non-Codex, and structurally incomplete
Host Auth entries before calculating the authoritative highest CPA priority
tier. Binding, admission, background refresh, and immutable scheduler snapshots
therefore use the same eligible roster.

The new `exclude_free_accounts` setting defaults to `true`. Only a normalized
plan value exactly equal to `free` is free; an empty plan is unknown, and every
other non-empty value, including `trial`, `team`, `plus`, and `pro`, is known
non-free. When a request contains a known non-free admitted candidate, the
plugin actively selects only among known non-free candidates. Unknown plans are
not actively selected. If every admitted candidate in the request is known
free, the plugin relaxes the filter for that request and selects a free account
using the existing availability and strategy order.

The configured CPA fallback remains unchanged and may still select an original
free or unknown candidate. To restore the v0.3.x active-selection behavior, set
`exclude_free_accounts: false`; no state-file deletion or host protocol change
is required.

## v0.3.1 strategy loading and diagnostics fix

v0.3.1 makes the two strategy fields presence-aware. Configuration is merged
in this order: defaults, persisted Management settings, then strategy fields
explicitly present in lifecycle YAML. Omitting `selection_strategy` or
`subscription_order` retains its persisted value. Setting either field to an
explicit empty value or `null` overrides its effective value for the current
lifecycle; an empty strategy restores the legacy `monthly_mode` ordering. This
override does not delete the saved Management setting, so a later lifecycle
configuration that omits the field restores the persisted value.

When Management settings are saved, validated configuration, persisted user
data, and immutable scheduler snapshots are committed consistently. While CPA
has not confirmed an authoritative roster, the scheduler remains in
`WaitingRoster` and uses the configured fallback without discarding the selected
strategy. Status and logs distinguish roster, candidate, admission, and ordering
outcomes with fixed non-sensitive reason categories. A successful authoritative
roster sync resumes the loaded strategy without requiring reconfiguration.

## v0.3.0 configurable selection strategies

v0.3.0 adds five explicit account-selection strategies while keeping the
existing order when `selection_strategy` is empty:

- `quota_high`: prefer the largest known bottleneck remaining quota.
- `quota_low`: prefer the smallest known bottleneck remaining quota.
- `subscription_high`: prefer the highest recognized subscription rank.
- `subscription_low`: prefer the lowest recognized subscription rank.
- `expiry_soon`: prefer the nearest known future subscription expiry.

The bottleneck quota is the minimum remaining percentage across the known
five-hour and long primary quota windows. Unknown quota, subscription, or
future-expiry data remains eligible but sorts after known values.

The fixed ordering layers are: CPA auth priority admission, availability class,
per-account plugin priority, configured selection strategy, then Auth ID.
`subscription_order` lists plan names from low to high and is required for both
subscription strategies. Values are trimmed, lowercased, and must be unique.

To roll back immediately, clear `selection_strategy`. The scheduler then uses
the v0.2.x `monthly_mode`, quota reset/expiry, remaining quota, and Auth ID
ordering. `monthly_mode` remains saved while a new strategy is active.

## v0.2.0 upgrade

v0.2.0 automatically migrates the legacy state filename to `.user-data.json`
and retains the original for one version cycle as `<legacy-name>.migrated`.
Machine-owned runtime state is stored separately in `.runtime-state.json` in
the same directory.

`codex-quota-scheduler` is a CLIProxyAPI (CPA) dynamic library plugin that
improves Codex account selection with an optimized Fill First scheduler.

The plugin keeps CPA's own auth priority as the first ordering rule. Within the
active CPA priority tier, it refreshes Codex quota status, tracks exhausted or
temporarily unavailable accounts, and picks the next account by availability and
reset or expiry time.

## v0.1.6 Priority Isolation

Version `0.1.6` loads only the maximum observed CPA priority tier from the
candidates CPA provides. Set every Codex account managed by this plugin to the
same CPA priority; `0` is recommended. Lower CPA priority tiers are not loaded,
refreshed, displayed, or scheduled by the plugin.

This is an operational requirement until CPA's built-in fallback can continue
past an exhausted maximum auth-priority tier. Track the upstream behavior in
[CLIProxyAPI issue #4196](https://github.com/router-for-me/CLIProxyAPI/issues/4196)
and the plugin isolation work in
[codex-quota-scheduler issue #2](https://github.com/JefferyZhang2019/cpa-plugin-codex-quota-scheduler/issues/2).

Plugin priority is independent of CPA priority and defaults to `0`. Higher
plugin-priority accounts are considered first, but the plugin falls through
exhausted internal tiers to the first usable lower plugin-priority tier before
using fallback. Plugin priority never reads from or writes to CPA.

## Features

- Scheduler plugin for CPA Codex accounts.
- Usage feedback handling for `usage_limit_reached` responses without counting
  quota exhaustion as a circuit-breaker failure.
- Five-hour, weekly, monthly, and reset-credit quota display when available.
- Circuit breaker state for repeated account failures.
- Bilingual Management UI: English and Chinese, with browser-language detection
  and a manual language selector.
- Account aliases, notes, tags, and groups stored in the plugin's local state.
- JSON export and import for scheduler settings and annotations.
- Release packaging for Linux, macOS, Windows, and FreeBSD.

## Privacy And Data Disclosure

This plugin runs inside CPA and uses CPA-provided host callbacks plus
plugin-owned CPA Management API routes. The browser resource page is only the UI
surface. State-changing actions require the CPA Management key and are sent to
`/v0/management/plugins/codex-quota-scheduler/...`. The page keeps that key only
in the current browser page session and does not write it to plugin state,
exports, logs, `localStorage`, or `sessionStorage`. The plugin does not run an
external service and does not send data to the plugin author.

The plugin may send authenticated requests to ChatGPT's quota and reset-credit
endpoints:

```text
GET https://chatgpt.com/backend-api/wham/usage
GET https://chatgpt.com/backend-api/wham/rate-limit-reset-credits
```

Those requests use the Codex credentials already configured in CPA. The plugin
uses the responses to calculate account quota state, reset-credit availability,
and scheduling order.

The plugin stores local state in CPA's plugin state area. Stored data can
include scheduler settings, recent quota snapshots, logs, aliases, notes, tags,
and group names. Do not put secrets in account notes, group notes, aliases, or
tags. The Management UI avoids rendering access tokens, authorization headers,
cookies, and other credential fields.

## Installation

Download the zip for your platform from the latest GitHub release:

```text
codex-quota-scheduler_<version>_<goos>_<goarch>.zip
```

Extract the dynamic library and place it in CPA's plugin directory for your
platform. The zip contains the library at the archive root:

- macOS: `codex-quota-scheduler.dylib`
- Linux and FreeBSD: `codex-quota-scheduler.so`
- Windows: `codex-quota-scheduler.dll`

Example:

```bash
mkdir -p /path/to/CLIProxyAPI/plugins/darwin/arm64
cp codex-quota-scheduler.dylib /path/to/CLIProxyAPI/plugins/darwin/arm64/
```

## CPA Configuration

Enable global plugins and this plugin in CPA:

```yaml
plugins:
  enabled: true
  configs:
    codex-quota-scheduler:
      enabled: true
      priority: 1 # CPA plugin registration/load priority
```

The `plugins.configs.codex-quota-scheduler.priority` value above is CPA's plugin
registration/load priority. It is unrelated to the per-account, plugin-owned
`scheduler_priority` edited in this plugin's Management UI, and it is also
separate from CPA auth priority on managed Codex accounts.

The plugin does not declare Management Center form fields. Scheduler settings,
aliases, notes, tags, and groups are edited from the plugin resource page and
persisted to the plugin state file.

The default scheduler settings are:

```yaml
handle_enabled: true
exclude_free_accounts: true
quota_refresh_interval: 30m
stale_after: 5h
refresh_active_window: 1h
refresh_after_reset_delay: 1m
refresh_retry_delays: 1m,5m,15m
refresh_on_startup: false
monthly_mode: expiry_order
selection_strategy: ""
subscription_order: []
fallback: fill-first
enable_usage_feedback: true
max_refresh_concurrency: 1
quota_endpoint: https://chatgpt.com/backend-api/wham/usage
circuit_failure_threshold: 5
circuit_open_duration: 30m
circuit_half_open_success_threshold: 2
max_log_entries: 200
log_retention: 24h
```

`exclude_free_accounts` is presence-aware across lifecycle YAML, persisted
Management settings, import, and export. An explicit lifecycle value overrides
the saved value for that lifecycle without deleting it; omitting the field
restores the persisted value. Old user data that lacks the field uses the safe
default `true`.

`monthly_mode` accepts:

- `expiry_order`: order monthly and weekly accounts by reset or expiry time
  within the same CPA priority tier.
- `priority`: prefer monthly accounts before weekly accounts within the same CPA
  priority tier.

`selection_strategy` accepts `quota_high`, `quota_low`, `subscription_high`,
`subscription_low`, or `expiry_soon`. Leave it empty for the legacy behavior.
For example, to prefer higher ranked subscriptions:

```yaml
selection_strategy: subscription_high
subscription_order:
  - free
  - plus
  - pro
```

Management settings persist across plugin lifecycle reconfiguration. Strategy
fields explicitly present in lifecycle YAML take precedence over those saved
settings. An omitted field keeps its persisted value; an explicit empty value
or `null` overrides it with an empty effective value for the current lifecycle
without deleting the saved Management setting. A later lifecycle configuration
that omits the field restores that saved value. An empty effective
`selection_strategy` selects the legacy order, and an empty effective
`subscription_order` is valid only when the effective strategy is not a
subscription strategy.

The Management UI shows the active strategy value for every account, including
the bottleneck quota, normalized plan/rank, subscription expiry, or an explicit
unknown state. Strategy changes are validated before the previous configuration
and immutable scheduler snapshot are replaced.

While the scheduler is inside `refresh_active_window`,
`quota_refresh_interval` is the normal per-account refresh cadence. The worker
refreshes only accounts that are due; it does not run a fixed full-account poll.
`stale_after` remains the maximum cache-age safety threshold. A
`usage_limit_reached` response immediately marks the selected account
temporarily exhausted until its reported reset time (or for 2 minutes when no
reset time is reported) and does not increment the circuit breaker. Reset
timestamps already consumed by a successful refresh are one-shot triggers; a
repeated upstream timestamp cannot create a two-second refresh loop.

## Management UI

### Authoritative roster lifecycle

Only the highest CPA priority tier reported by the host is active; accounts at
lower CPA priorities are not loaded into scheduler or Management payloads.
Using equal CPA priorities (preferably `0`) is recommended when all Codex
accounts should participate.

When the host cannot confirm priorities (Capability B), restart recovery keeps
normal refresh Dormant and Probe windows in `WaitingRoster`. The
`probe_on_provisional_roster` setting is an explicit risk option and defaults to
`false`; provisional data never becomes authoritative merely because an account
appears among scheduler candidates. A later successful authoritative roster
sync automatically recovers Capability B to Capability A and resumes the
already loaded selection strategy without reconfiguration. During
`WaitingRoster`, fallback diagnostics retain the effective strategy and report
zero authoritative admissions instead of treating scheduler candidates as an
authoritative roster.

Normal quota refresh makes no real requests while Dormant. Probe scheduling is
independent and may pre-wake roster synchronization before a due window.

Open the resource page from CPA's plugin resources, or visit:

```text
/v0/resource/plugins/codex-quota-scheduler/status
```

The page follows the browser language by default and can be switched between
English and Chinese manually. Settings, buttons, account cards, common status
text, and new UI log messages follow the selected language.

The resource page asks for the CPA Management key before it performs protected
actions such as saving settings, importing state, editing annotations, viewing
logs through the API, or requesting quota refresh. This follows CPA's security
boundary: `/v0/resource/plugins/...` serves the browser resource page, while
`/v0/management/...` handles authenticated management operations.

The resource page under `/v0/resource/plugins/codex-quota-scheduler/status`
serves UI content only. Settings, import/export, annotations, logs, and refresh
actions use `/v0/management/plugins/codex-quota-scheduler/...` and require the
CPA Management key. The quota endpoint is restricted to
`https://chatgpt.com/backend-api/wham/usage`.

The page provides:

- Scheduler settings.
- Sorted account queue.
- Separate CPA priority and plugin priority badges.
- Per-account plugin priority editing. Plugin priority defaults to `0`, is
  independent of CPA, and falls through exhausted internal tiers.
- Quota bars and reset times.
- Circuit breaker state.
- Account and group annotations.
- Log viewing and export.
- Configuration export and import.
- English and Chinese interface switching.

## Build

Requirements:

- Go 1.26 or newer, as declared by `go.mod`.
- CGO support.
- A C compiler for `-buildmode=c-shared`.
- `make` for the cross-platform release workflow.

Run tests:

```bash
make test
```

Build the dynamic library for the current platform:

```bash
make build
```

Build and package the release zip:

```bash
make package VERSION=0.4.0
```

Generate an aggregate checksum file for local release assets:

```bash
make checksums VERSION=0.4.0
```

Windows users can also use the PowerShell helper:

```powershell
.\build.ps1
```

The PowerShell script builds `dist/codex-quota-scheduler.dll` and requires a C
compiler such as MinGW-w64 on `PATH`.

## GitHub Releases

Version `0.4.0` adds authoritative roster eligibility filtering and the
default-on free/unknown active-selection policy. Version `0.3.1` fixes lifecycle strategy loading, atomic configuration
publication, and safe `WaitingRoster` diagnostics. Version `0.3.0` adds the five
explicit account-selection strategies. Version `0.2.0` completes the
spec-driven scheduler refactor, including
authoritative roster lifecycle handling, persisted single-lease reset probes,
Codex quota-window compatibility, availability-ordered management queues, and
bilingual settings guidance. Historical version `0.1.6` isolates CPA priority
admission to the maximum observed tier, adds plugin-owned per-account priority
with internal exhausted-tier fallthrough, and consumes successful reset-trigger
refreshes once. Version `0.1.5` restored interval-based per-account refreshes
inside the active window and kept quota-exhaustion feedback separate from
circuit-breaker failures. Version `0.1.4` keeps account cards, logs, refresh actions, scheduler
status, and reset-probe notices behind the CPA Management key. Version `0.1.3`
adds the opt-in automatic reset probe for lazy Codex quota windows. Version
`0.1.2` adds adaptive refresh scheduling and a dynamically updating bilingual
UI. Version `0.1.1` moves all state-changing and privileged operations behind
CPA Management API routes and restricts `quota_endpoint` to the expected
ChatGPT quota endpoint. Version `0.1.0` was the first public release version for
this repository. GitHub Actions builds release assets when a tag matching `v*`
is pushed. Use a dotted numeric version tag such as:

```bash
git tag -a v0.4.0 -m "v0.4.0"
git push origin v0.4.0
```

The `Build` workflow runs tests and creates the release automatically. Release
assets are named:

```text
codex-quota-scheduler_<version>_<goos>_<goarch>.zip
checksums.txt
```

For `v0.4.0`, the expected platform assets are:

- `codex-quota-scheduler_0.4.0_darwin_amd64.zip`
- `codex-quota-scheduler_0.4.0_darwin_arm64.zip`
- `codex-quota-scheduler_0.4.0_freebsd_amd64.zip`
- `codex-quota-scheduler_0.4.0_linux_amd64.zip`
- `codex-quota-scheduler_0.4.0_linux_arm64.zip`
- `codex-quota-scheduler_0.4.0_windows_amd64.zip`
- `codex-quota-scheduler_0.4.0_windows_arm64.zip`
- `checksums.txt`

`checksums.txt` uses sha256sum format:

```text
<sha256>  codex-quota-scheduler_0.4.0_darwin_arm64.zip
```

## Management API

The plugin resource page is served from:

```text
GET /v0/resource/plugins/codex-quota-scheduler/status
```

That resource route must not be used as a write-action API. All state-changing
or privileged operations are exposed through CPA Management API routes and
require the Management key:

```text
GET  /v0/management/plugins/codex-quota-scheduler/status?format=json
GET  /v0/management/plugins/codex-quota-scheduler/logs
GET  /v0/management/plugins/codex-quota-scheduler/export
PUT  /v0/management/plugins/codex-quota-scheduler/settings
POST /v0/management/plugins/codex-quota-scheduler/refresh
POST /v0/management/plugins/codex-quota-scheduler/refresh/account
POST /v0/management/plugins/codex-quota-scheduler/import
PUT  /v0/management/plugins/codex-quota-scheduler/annotations
PATCH /v0/management/plugins/codex-quota-scheduler/annotations/account
PATCH /v0/management/plugins/codex-quota-scheduler/annotations/group
```

For quota refresh safety, `quota_endpoint` is restricted to:

```text
https://chatgpt.com/backend-api/wham/usage
```

## License

MIT License. See [LICENSE](LICENSE).
