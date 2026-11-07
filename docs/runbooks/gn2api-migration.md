# Migrating GN2API to this fork

Base: `zuchengchen/sub2api` tag `czc-v2026.11.07.1`, commit
`eb0eba8c02dc36a78fdb94ab4a1092db82ab372d`.

This is a cross-fork migration, not a routine binary update. Do not start a
candidate against the production database to discover compatibility problems.

## Validation and packaging

Use the `Verified Linux amd64` GitHub Actions workflow. It runs backend unit
tests, existing migration/billing/account integration tests, frontend lint,
frontend tests, an embedded frontend build, and a migration rehearsal against
a schema-only export with synthetic business rows. The schema is supplied as
encrypted repository secrets. No production credentials or row data belong in
the repository, workflow logs, or artifacts.

The schema rehearsal preserves fingerprints of synthetic users, API keys,
subscriptions, payments, historical usage and branding. It also checks repeat
migration idempotence and preservation of historical migration checksums.
This does not establish recovery of a full production backup or behavior on
every production row; those require a separate isolated server rehearsal.

Only Linux amd64 is built. Release archives contain exactly `sub2api`,
`LICENSE`, `config.example.yaml`, and `sub2api.service`, plus a separately
published SHA-256 checksum file. The deployment must preserve the existing
configuration and service unit rather than installing the examples.

## Compatibility changes

- Accept only the reviewed GN2API and CZC checksums of
  `237_add_minimax_platform.sql`; never rewrite the stored checksum or relax
  checking for arbitrary migrations.
- Migration 273 restores the final platform constraint state intended by
  migration 242, which GN2API may already have recorded before CZC's earlier
  migrations recreate those constraints. Application validation remains active.
- Updates and the running release link refer to `Gnsvip/sub2api-czc`. Dated
  CZC tags use the numeric application version from the release title.

## Upstream behavior changes to disclose

The target retires Gemini/Antigravity, scheduled account tests and Spark shadow
accounts. Its migrations disable affected accounts/groups and remove related
unsupported monitoring/routes/quotas. It also changes OpenAI protection,
WebSocket defaults and long-context billing defaults. Review account settings
and prices before switching traffic. Existing billing rows are not to be
recalculated. Preserve all data in a verified backup, including retired data.

## Production sequence

1. Verify the release artifact and source commit. Back up the binary, config,
   service unit, Nginx configuration, branding files and complete database.
2. Restore a database copy in an isolated network with no live upstream access,
   email, payment callbacks or production Redis access. Confirm migration and
   business-data invariants, then test the candidate application's startup.
3. Notify the owner of the exact changes, downtime estimate and rollback plan
   before deployment. Do not infer successful verification from a running job.
4. Drain traffic in the maintenance window, capture the final consistent data
   state, then migrate and atomically replace the binary. Preserve the current
   service unit, configuration and branding. Restart only Sub2API.
5. Verify version, release URL, health, user/admin pages, billing invariants,
   streaming behavior, and the installed/running binary hashes before reopening.

## Branding and rollback

Keep `/opt/sub2api-custom/logo.png`, `/opt/sub2api-custom/favicon.ico`, existing
Nginx logo/favicon aliases, and database `site_name`/`site_logo` settings.
Verify both file hashes and served image bytes, then visually check the page.
Check any injected custom JavaScript against the target frontend.

For this migration a binary rollback alone is insufficient: restore the
matching pre-migration database and configuration before reopening traffic.
After new business writes are accepted, do not blindly restore an old snapshot;
reconcile new payments and usage first. A rollback rehearsal must establish the
actual restore time before quoting a maintenance-window estimate.
