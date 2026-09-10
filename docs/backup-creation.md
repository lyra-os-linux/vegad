# Recovering an interrupted backup configuration

Creating a configuration reserves its ID in `configs/<id>.pending` before
preparing its credential, restic repository and systemd units. The final JSON
is published only after preparation; activation is the last step. Failures
retain the pending record and can be resumed by repeating `CreateConfig` with
the same parameters, including after a daemon restart. Different parameters
cannot reuse a pending ID. The error reports the failed step.

Retries reuse the repository and credential. They never delete repository data
or roll back a repository that may already contain snapshots. Existing units
unrelated to a pending creation prevent a new creation with the same ID.
Administrators should not modify pending artifacts before resuming the request.

New credentials are persisted atomically in root-owned mode-0600 files under
`/etc/vega/backup/passwords`, so later systemd jobs do not require an unlocked
session keyring. Existing credentials in Secret Service remain readable; when
resuming creation, an available existing secret is copied to the protected
password file. Keep credentials when recovering or moving a repository.

An unavailable restic executable now leaves creation pending with an explicit
error. On-connect configurations may defer repository preparation until their
destination is mounted. A failure while enabling a schedule can leave the
prepared configuration and units active; retrying finishes activation and clears
the pending record.
