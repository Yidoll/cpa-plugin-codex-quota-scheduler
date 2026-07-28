# Changelog

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
