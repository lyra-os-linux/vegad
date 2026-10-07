package parental

import (
	"errors"
	"strings"
	"testing"
)

func TestResolveFlatpakDeploymentFailsClosed(t *testing.T) {
	request := FlatpakLaunchRequest{AppID: "org.lyra.TestApp"}
	binding := FlatpakPeerBinding{Association: strings.Repeat("9", 32), Generation: strings.Repeat("a", 32),
		CatalogDigest: strings.Repeat("b", 64), CatalogRevision: 1,
		UID: 1003, GID: 100, SELinuxContext: "system_u:system_r:lyra_test_child_a_t:s0"}
	publisher := strings.Repeat("c", 40)
	approval := FlatpakApproval{Ref: "app/org.lyra.TestApp/x86_64/stable", Scope: "system",
		Installation: "system", Origin: "lyra-test", PublisherFingerprint: publisher}
	installed := InstalledFlatpak{Ref: approval.Ref, Scope: approval.Scope, Installation: approval.Installation,
		Origin: approval.Origin, PublisherFingerprint: publisher, Commit: strings.Repeat("d", 64),
		RuntimeRef: "runtime/org.lyra.Platform/x86_64/stable", RuntimeCommit: strings.Repeat("e", 64)}
	got, err := ResolveFlatpakDeployment(request, binding, approval, []InstalledFlatpak{installed})
	if err != nil || got.InstalledFlatpak != installed {
		t.Fatalf("signed deployment failed: %+v %v", got, err)
	}
	updated := installed
	updated.Commit = strings.Repeat("f", 64)
	if _, err := ResolveFlatpakDeployment(request, binding, approval, []InstalledFlatpak{updated}); err != nil {
		t.Fatalf("same publisher update rejected: %v", err)
	}
	unsigned := approval
	unsigned.PublisherFingerprint = ""
	unsigned.Commit = installed.Commit
	unsignedInstall := installed
	unsignedInstall.PublisherFingerprint = ""
	if _, err := ResolveFlatpakDeployment(request, binding, unsigned, []InstalledFlatpak{unsignedInstall}); err != nil {
		t.Fatalf("exact unsigned commit rejected: %v", err)
	}
	unsignedInstall.Commit = updated.Commit
	if _, err := ResolveFlatpakDeployment(request, binding, unsigned, []InstalledFlatpak{unsignedInstall}); !errors.Is(err, UnresolvedFlatpakDeployment) {
		t.Fatalf("unsigned update inherited approval: %v", err)
	}
	userApproval := approval
	userApproval.Scope, userApproval.Installation, userApproval.OwnerUID = "user", "user", binding.UID
	userInstall := installed
	userInstall.Scope, userInstall.Installation, userInstall.OwnerUID = "user", "user", binding.UID
	if _, err := ResolveFlatpakDeployment(request, binding, userApproval, []InstalledFlatpak{userInstall}); err != nil {
		t.Fatalf("owned user installation rejected: %v", err)
	}

	for name, mutate := range map[string]func(*FlatpakApproval, *[]InstalledFlatpak){
		"missing":   func(_ *FlatpakApproval, items *[]InstalledFlatpak) { *items = nil },
		"duplicate": func(_ *FlatpakApproval, items *[]InstalledFlatpak) { *items = append(*items, (*items)[0]) },
		"other-scope": func(_ *FlatpakApproval, items *[]InstalledFlatpak) {
			extra := (*items)[0]
			extra.Scope, extra.Installation, extra.OwnerUID = "user", "user", 1003
			*items = append(*items, extra)
		},
		"wrong-ref":     func(a *FlatpakApproval, _ *[]InstalledFlatpak) { a.Ref = "app/org.lyra.Other/x86_64/stable" },
		"wrong-branch":  func(a *FlatpakApproval, _ *[]InstalledFlatpak) { a.Ref = "app/org.lyra.TestApp/x86_64/beta" },
		"origin-change": func(_ *FlatpakApproval, items *[]InstalledFlatpak) { (*items)[0].Origin = "other" },
		"publisher-change": func(_ *FlatpakApproval, items *[]InstalledFlatpak) {
			(*items)[0].PublisherFingerprint = strings.Repeat("f", 40)
		},
		"short-commit":    func(_ *FlatpakApproval, items *[]InstalledFlatpak) { (*items)[0].Commit = strings.Repeat("d", 12) },
		"missing-runtime": func(_ *FlatpakApproval, items *[]InstalledFlatpak) { (*items)[0].RuntimeRef = "" },
		"short-runtime-commit": func(_ *FlatpakApproval, items *[]InstalledFlatpak) {
			(*items)[0].RuntimeCommit = strings.Repeat("e", 12)
		},
		"wrong-runtime-arch": func(_ *FlatpakApproval, items *[]InstalledFlatpak) {
			(*items)[0].RuntimeRef = "runtime/org.lyra.Platform/aarch64/stable"
		},
		"user-owner-mismatch": func(_ *FlatpakApproval, items *[]InstalledFlatpak) {
			(*items)[0].Scope, (*items)[0].Installation, (*items)[0].OwnerUID = "user", "user", 1011
		},
		"unknown-publisher": func(a *FlatpakApproval, _ *[]InstalledFlatpak) { a.PublisherFingerprint = "" },
		"malformed-ref": func(_ *FlatpakApproval, items *[]InstalledFlatpak) {
			(*items)[0].Ref = "app/org.lyra.TestApp/x86_64/../stable"
		},
	} {
		t.Run(name, func(t *testing.T) {
			a, items := approval, []InstalledFlatpak{installed}
			mutate(&a, &items)
			if _, err := ResolveFlatpakDeployment(request, binding, a, items); !errors.Is(err, UnresolvedFlatpakDeployment) {
				t.Fatalf("unsafe deployment accepted: %v", err)
			}
		})
	}
}
