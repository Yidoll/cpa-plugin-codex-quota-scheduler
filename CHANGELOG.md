# Changelog

## Unreleased

- Negotiate the host-advertised `emergency-provider-fill-first` Scheduler
  delegate while preserving ordinary `fill-first` on older hosts.
- Use `fallback: fill-first` as the master switch for the global emergency pool
  of enabled Codex API and OpenAI-compatible Providers after all authoritative
  OAuth accounts are unavailable.
- Keep OAuth ahead of every Provider priority and preserve deterministic
  Provider priority/Auth ID ordering, retry, SSE, and required WebSocket
  behavior.
- Add bilingual Management guidance, capability status, latest safe emergency
  result, aggregate diagnostics, and credential-leak regression tests.

### Compatibility and rollout

| Host | Plugin | Behavior |
| --- | --- | --- |
| New | Old | Existing OAuth and ordinary `fill-first` behavior is unchanged. |
| Old | New | The missing capability keeps ordinary `fill-first` and records `emergency_delegate_unsupported`. |
| New | New | OAuth remains preferred; the restricted global Provider exit is enabled by `fallback: fill-first`. |
| Old | Old | Existing behavior is unchanged. |

Release and deploy CLIProxyAPI first, then remove the development `replace`
directive and pin this plugin to the formal SDK release containing the new
delegate. Roll back in reverse order: plugin first, host second.

## 0.4.0

- Filter disabled, unavailable, non-Codex, empty-ID, and empty-auth-index Host
  Auth entries before calculating the authoritative highest CPA priority tier.
- Add default-on `exclude_free_accounts` across lifecycle YAML, Management,
  status, persistence, import, export, and the bilingual UI.
- Treat only normalized `free` as free, keep empty plans unknown, prefer known
  non-free candidates, and relax filtering when every eligible request
  candidate is known free.
- Keep free and unknown accounts in the authoritative roster and background
  refresh while making plan eligibility a snapshot-only scheduler decision.
- Add aggregate, non-sensitive roster and plan-filter diagnostics. Set
  `exclude_free_accounts: false` to restore v0.3.x active-selection behavior.

## 0.3.1

- Preserve explicitly configured lifecycle strategy fields over persisted
  Management settings while treating explicit empty values as a legacy reset.
- Commit validated configuration, persisted user data, and scheduler snapshots
  as one consistent update.
- Keep the configured strategy while waiting for an authoritative roster and
  report safe candidate, admission, ordering, and fallback diagnostics.
- Track authoritative roster synchronization with fixed, non-sensitive result
  and error categories.

## 0.3.0

- Add quota, subscription-rank, and subscription-expiry selection strategies.
- Keep empty `selection_strategy` fully compatible with the v0.2.x legacy order.
- Add normalized `subscription_order` validation and atomic runtime updates.
- Publish non-sensitive strategy values in status, logs, and the bilingual UI.
- Update CLIProxyAPI Plugin ABI v1 SDK dependency to v7.2.102.
