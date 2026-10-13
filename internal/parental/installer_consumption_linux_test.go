package parental

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestInstalledESPProvisionerUsesFixedTrustedPaths(t *testing.T) {
	permission := NewESPInstallerPermission()
	if permission.directory != InstalledESPEnrollmentPermitDirectory ||
		permission.authority != InstalledParentalAuthorityDirectory ||
		permission.catalog != InstalledParentalCatalogDirectory ||
		permission.inventory != InstalledParentalInventoryDirectory ||
		permission.ledger != InstalledParentalLedgerDirectory ||
		permission.owner != 0 {
		t.Fatalf("unexpected ESP permission configuration: %#v", permission)
	}
	domain := installedParentalSnapperDomain()
	if domain == nil || domain.scope != InstalledParentalSnapshotScope || domain.config != InstalledParentalSnapperConfig ||
		filepath.Dir(InstalledParentalAuthorityDirectory) != "/var/lib/lyra" ||
		filepath.Dir(InstalledParentalCatalogDirectory) != InstalledParentalSnapshotScope ||
		filepath.Dir(InstalledParentalInventoryDirectory) != InstalledParentalSnapshotScope ||
		filepath.Dir(InstalledParentalLedgerDirectory) != InstalledParentalSnapshotScope {
		t.Fatal("installed parental paths do not match the fixed Snapper scope and authority sibling")
	}
}

// Fixture consumption is not a production installer receipt implementation.
func TestInstallerPermissionConsumedBeforeCommit(t *testing.T) {
	for _, mode := range []string{"success", "missing-consumer", "consume-failure", "still-fresh", "revoked-maintenance", "cut-after-consume"} {
		t.Run(mode, func(t *testing.T) {
			base, source, initial, dir, op := provisionFixture(t)
			spent, called := false, 0
			check := func(installation, operation, digest string) error {
				if installation != initial.installation || operation != op || digest != initial.digest {
					return Denied
				}
				if spent {
					return InstallerPermissionConsumed
				}
				return nil
			}
			consume := func(installation, operation, digest string) error {
				called++
				if installation != initial.installation || operation != op || digest != initial.digest {
					t.Fatal("unbound consumption")
				}
				for _, name := range []string{enrollmentName, "reference.json", "current.json"} {
					if _, e := os.Stat(filepath.Join(dir, name)); e != nil {
						t.Fatal("consumption before durable prefix", name, e)
					}
				}
				if _, e := os.Lstat(filepath.Join(dir, enrollmentCommitName)); !os.IsNotExist(e) {
					t.Fatal("consumption after completion", e)
				}
				if mode == "consume-failure" {
					return errors.New("fixture durable authority failure")
				}
				if mode != "still-fresh" {
					spent = true
				}
				return nil
			}
			if mode == "missing-consumer" {
				consume = nil
			}
			p := NewInstallerReferenceProvisioner(dir, source.catalogDirectory, source.issuer, source.qualify, check, consume)
			p.writer.owner = base.writer.owner
			maintenance := func() error {
				if mode == "revoked-maintenance" && spent {
					return Denied
				}
				return nil
			}
			if mode == "cut-after-consume" {
				cutProvision(t, p, initial, op, "installer:consumed")
				got, e := p.ResumeInitial(initial, op, maintenanceFixture)
				if e != nil || got.digest != initial.digest {
					t.Fatal(got, e)
				}
			} else {
				got, e := p.Initialize(initial, op, maintenance)
				if mode == "success" {
					if e != nil || got.digest != initial.digest {
						t.Fatal(got, e)
					}
				} else {
					want := PublicationUncertain
					if mode == "missing-consumer" {
						want = Denied
					}
					assertCode(t, e, want)
					if got.digest != "" {
						t.Fatal("acknowledged incomplete issuance")
					}
				}
			}
			if mode == "missing-consumer" {
				if called != 0 {
					t.Fatal("called absent consumer")
				}
				entries, _ := os.ReadDir(dir)
				if len(entries) != 1 {
					t.Fatal("unauthorized writes", entries)
				}
				return
			}
			if called != 1 {
				t.Fatal("consumption count", called)
			}
			_, e := source.Snapshot(initial.installation)
			if mode == "success" || mode == "cut-after-consume" {
				if e != nil {
					t.Fatal(e)
				}
				// Loss of ALL enrollment files is still denied by the consumed proof.
				for _, name := range []string{enrollmentName, enrollmentCommitName, "reference.json", "current.json"} {
					if e := os.Remove(filepath.Join(dir, name)); e != nil {
						t.Fatal(e)
					}
				}
			} else {
				assertCode(t, e, RecoveryRequired)
			}
			if spent {
				_, e = p.Initialize(initial, op, maintenanceFixture)
				assertCode(t, e, Denied)
				maintenance := func() error {
					if mode == "revoked-maintenance" {
						return Denied
					}
					return nil
				}
				_, e = p.ResumeInitial(initial, op, maintenance)
				if mode == "revoked-maintenance" {
					assertCode(t, e, Denied)
					got, resumeErr := p.ResumeInitial(initial, op, maintenanceFixture)
					if resumeErr != nil || got.digest != initial.digest {
						t.Fatal(got, resumeErr)
					}
				} else {
					assertCode(t, e, RecoveryRequired)
				}
			}
		})
	}
}
