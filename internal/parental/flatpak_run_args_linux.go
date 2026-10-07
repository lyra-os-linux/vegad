package parental

import "strings"

// BuildFlatpakRunArgs produces only server-selected options. It does not
// authorize or execute anything: the caller must hold the admission/revocation
// gate, use a trusted inventory and prevent direct launch from child domains.
// No client argv, path, runtime override or FD is accepted here.
func BuildFlatpakRunArgs(resolved ResolvedFlatpak, childUID uint32) ([]string, error) {
	app := resolved.InstalledFlatpak
	appID, ok := flatpakFullRef(app.Ref, "app")
	if !ok || !validFlatpakScope(app.Scope, app.Installation, app.OwnerUID, childUID) ||
		!hash.MatchString(app.Commit) || !hash.MatchString(app.RuntimeCommit) {
		return nil, UnresolvedFlatpakDeployment
	}
	runtimeID, ok := flatpakFullRef(app.RuntimeRef, "runtime")
	if !ok {
		return nil, UnresolvedFlatpakDeployment
	}
	appParts, runtimeParts := strings.Split(app.Ref, "/"), strings.Split(app.RuntimeRef, "/")
	if appParts[2] != runtimeParts[2] {
		return nil, UnresolvedFlatpakDeployment
	}
	args := []string{"run"}
	if app.Scope == "user" {
		args = append(args, "--user")
	} else if app.Installation == "system" {
		args = append(args, "--system")
	} else {
		args = append(args, "--installation="+app.Installation)
	}
	args = append(args, "--arch="+appParts[2], "--branch="+appParts[3],
		"--runtime="+runtimeID, "--runtime-version="+runtimeParts[3],
		"--commit="+app.Commit, "--runtime-commit="+app.RuntimeCommit, appID)
	return args, nil
}
