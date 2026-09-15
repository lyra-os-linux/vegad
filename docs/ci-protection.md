# Required checks for main

The `main` branch requires the GitHub Actions checks `go` and
`system-contracts` (app ID `15368`). Branches must be up to date before merge.
The policy also applies to repository administrators. Force pushes and branch
deletion remain disabled; pull requests do not require an additional reviewer.

`go` checks formatting, static analysis and Go tests. `system-contracts`
validates RPM/systemd/Polkit contracts and the real D-Bus wire interface in an
openSUSE container. The live-system and release qualification gates remain
separate.

## Qualification on 2026-09-15

[PR #52](https://github.com/lyra-os-linux/vegad/pull/52) exercised the policy
using the maintainer's administrator account. A temporary step, restricted to
that PR branch, delayed and then deliberately failed the `go` job. No daemon
code was changed. The step was removed before the final CI run; the final
workflow is identical to the previously qualified production workflow.

| State on the controlled PR | Merge API result |
| --- | --- |
| Both required checks in progress | HTTP 405: `2 of 2 required status checks are in progress.` |
| `go` failed, `system-contracts` still running | HTTP 405: `2 of 2 required status checks have not succeeded: 1 failing.` |

The merge requests included the exact PR head SHA. The blocked attempts left
`main` at `fdd953755311251ae02efee3c0edf14c2891871f`. The
[controlled CI run](https://github.com/lyra-os-linux/vegad/actions/runs/34982699341)
records the intentional failure; it is not a product regression. Successful
completion of both jobs on the final PR head remains required for the
documentation merge. The issue records the final CI and merge receipt.

## Administration and recovery

Administrators can still deliberately edit repository protection settings;
this policy removes their routine merge bypass, not their administration
rights. No user/team/app bypass has been configured, and no repository ruleset
adds exceptions in this qualification. Recheck effective rules if organization
policy, custom roles or workflow names change. GitHub accepts successful,
neutral or skipped conclusions for required checks; both current jobs run on
every PR without job-level skip conditions. Preserve that coverage when
editing the workflows.

The effective policy is a GitHub repository setting, not configuration loaded
from this document. Inspect `GET /repos/lyra-os-linux/vegad/branches/main/protection`
and the rules applicable to `main` after changes. Preserve the GitHub Actions
app binding when renaming a required job. If the workflow or infrastructure
fails, correct it and rerun the checks before merging.

For an explicitly approved rollback of this policy, the previous configuration
had no required status checks and did not enforce protection for administrators;
the other fields listed above were unchanged. Changing those two fields would
remove this CI gate and must be recorded as a policy change.

References:

- https://github.com/lyra-os-linux/vegad/issues/37
- https://docs.github.com/en/rest/branches/branch-protection#update-branch-protection
