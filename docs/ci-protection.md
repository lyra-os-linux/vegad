# Required checks for main

The `main` branch requires the GitHub Actions checks `go` and
`system-contracts` (app ID `15368`). Branches must be up to date before merge.
The policy also applies to repository administrators. Force pushes and branch
deletion remain disabled; pull requests do not require an additional reviewer.

`go` checks formatting, static analysis and Go tests. `system-contracts`
validates RPM/systemd/Polkit contracts and the real D-Bus wire interface in an
openSUSE container. The live-system and release qualification gates remain
separate.

The branch-protection qualification for issue #37 uses a temporary failure
step restricted to its test pull request. That step must be removed before
integrating the documentation. Pending/failing merge attempts and the final
successful checks are recorded on the issue.

Administrators can still deliberately edit repository protection settings;
this policy removes their routine merge bypass, not their administration
rights. No user/team/app bypass has been configured. Recheck effective rules
if organization policy, custom roles or workflow names change.

References:

- https://github.com/lyra-os-linux/vegad/issues/37
- https://docs.github.com/en/rest/branches/branch-protection#update-branch-protection
