package parental

import (
	"context"
	"errors"
	"strconv"
	"strings"
	"testing"
)

type bootstrapPermit struct {
	installation, operation string
	consumed                bool
	trace                   *[]string
	err                     error
}

func (p *bootstrapPermit) CurrentEnrollment() (string, string, bool, error) {
	*p.trace = append(*p.trace, "permit")
	return p.installation, p.operation, p.consumed, p.err
}

type bootstrapFetcher struct {
	issuer, fingerprint string
	issued              SignedFirstReference
	trace               *[]string
	err                 error
}

func (f *bootstrapFetcher) IssuerIdentity() (string, string) { return f.issuer, f.fingerprint }

func (f *bootstrapFetcher) Fetch(_ context.Context, installation, operation string) (SignedFirstReference, error) {
	*f.trace = append(*f.trace, "fetch:"+installation+":"+operation)
	return f.issued, f.err
}

type bootstrapWriter struct {
	issuer               string
	trace                *[]string
	initialized, resumed int
	initial              RecoveryReference
	operation            string
	revalidate           func() error
	err                  error
}

func (w *bootstrapWriter) Issuer() string { return w.issuer }

func (w *bootstrapWriter) Initialize(initial RecoveryReference, operation string, revalidate func() error) (RecoveryReference, error) {
	*w.trace = append(*w.trace, "initialize")
	w.initialized++
	w.initial, w.operation, w.revalidate = initial, operation, revalidate
	return initial, w.err
}

func (w *bootstrapWriter) ResumeInitial(initial RecoveryReference, operation string, revalidate func() error) (RecoveryReference, error) {
	*w.trace = append(*w.trace, "resume")
	w.resumed++
	w.initial, w.operation, w.revalidate = initial, operation, revalidate
	return initial, w.err
}

func newBootstrapFixture(t *testing.T, trace *[]string, consumed bool) (*FirstEnrollmentBootstrap, *bootstrapWriter, RecoveryReference) {
	t.Helper()
	installation, operation, issuer, fingerprint := strings.Repeat("1", 32), strings.Repeat("2", 32), strings.Repeat("a", 32), strings.Repeat("B", 40)
	row := referenceRow("first-install")
	row["installation"] = installation
	initial := decodeReference(t, row)
	issued := SignedFirstReference{issuer: issuer, fingerprint: fingerprint, operation: operation, reference: initial}
	permission := &bootstrapPermit{installation: installation, operation: operation, consumed: consumed, trace: trace}
	fetcher := &bootstrapFetcher{issuer: issuer, fingerprint: fingerprint, issued: issued, trace: trace}
	writer := &bootstrapWriter{issuer: issuer, trace: trace}
	revalidate := func() error { *trace = append(*trace, "revalidate"); return nil }
	bootstrap := NewFirstEnrollmentBootstrap(issuer, fingerprint, permission, fetcher, writer,
		func() (bool, error) { *trace = append(*trace, "authorize"); return true, nil }, revalidate)
	return bootstrap, writer, initial
}

func TestFirstEnrollmentBootstrapAuthorizesBeforeFetchingAndInitializes(t *testing.T) {
	trace := []string{}
	bootstrap, writer, initial := newBootstrapFixture(t, &trace, false)
	got, err := bootstrap.Run(context.Background())
	if err != nil || got.Digest() != initial.Digest() {
		t.Fatalf("Run() = %#v, %v", got, err)
	}
	want := []string{"authorize", "revalidate", "permit", "revalidate", "fetch:" + strings.Repeat("1", 32) + ":" + strings.Repeat("2", 32), "revalidate", "initialize"}
	if strings.Join(trace, ",") != strings.Join(want, ",") || writer.initialized != 1 || writer.resumed != 0 || writer.operation != strings.Repeat("2", 32) || writer.revalidate == nil {
		t.Fatalf("trace=%v writer=%#v", trace, writer)
	}
}

func TestFirstEnrollmentBootstrapResumesOnlyConsumedOperation(t *testing.T) {
	trace := []string{}
	bootstrap, writer, _ := newBootstrapFixture(t, &trace, true)
	if _, err := bootstrap.Run(context.Background()); err != nil {
		t.Fatal(err)
	}
	if writer.initialized != 0 || writer.resumed != 1 || trace[len(trace)-1] != "resume" {
		t.Fatalf("consumed permit did not select resume: trace=%v writer=%#v", trace, writer)
	}
}

func TestFirstEnrollmentBootstrapDenialDoesNotReadPermitOrContactIssuer(t *testing.T) {
	trace := []string{}
	bootstrap, writer, _ := newBootstrapFixture(t, &trace, false)
	bootstrap.authorize = func() (bool, error) { trace = append(trace, "authorize"); return false, nil }
	if _, err := bootstrap.Run(context.Background()); err != Denied {
		t.Fatalf("Run error=%v, want Denied", err)
	}
	if strings.Join(trace, ",") != "authorize" || writer.initialized != 0 || writer.resumed != 0 {
		t.Fatalf("denied request had side effects: trace=%v writer=%#v", trace, writer)
	}
}

func TestFirstEnrollmentBootstrapRejectsIssuerMismatchBeforeWriting(t *testing.T) {
	trace := []string{}
	bootstrap, writer, _ := newBootstrapFixture(t, &trace, false)
	bootstrap.fetcher.(*bootstrapFetcher).issued.operation = strings.Repeat("3", 32)
	if _, err := bootstrap.Run(context.Background()); err != InvalidReference {
		t.Fatalf("Run error=%v, want InvalidReference", err)
	}
	if writer.initialized != 0 || writer.resumed != 0 {
		t.Fatal("mismatched issuer result reached provisioner")
	}
}

func TestFirstEnrollmentBootstrapRejectsConfigurationMismatchBeforeAuthorization(t *testing.T) {
	trace := []string{}
	bootstrap, writer, _ := newBootstrapFixture(t, &trace, false)
	bootstrap.fetcher.(*bootstrapFetcher).fingerprint = strings.Repeat("C", 40)
	if _, err := bootstrap.Run(context.Background()); err != Denied {
		t.Fatalf("Run error=%v, want Denied", err)
	}
	if len(trace) != 0 || writer.initialized != 0 || writer.resumed != 0 {
		t.Fatalf("configuration mismatch had side effects: trace=%v", trace)
	}
}

func TestFirstEnrollmentBootstrapRejectsCanceledContextBeforeAuthorization(t *testing.T) {
	trace := []string{}
	bootstrap, writer, _ := newBootstrapFixture(t, &trace, false)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := bootstrap.Run(ctx); err != context.Canceled {
		t.Fatalf("Run error=%v, want context.Canceled", err)
	}
	if len(trace) != 0 || writer.initialized != 0 || writer.resumed != 0 {
		t.Fatalf("canceled request had side effects: trace=%v", trace)
	}
}

func TestFirstEnrollmentBootstrapStopsWhenMaintenanceAuthorizationIsRevoked(t *testing.T) {
	for _, failedCheck := range []int{1, 2, 3} {
		t.Run(strconv.Itoa(failedCheck), func(t *testing.T) {
			trace := []string{}
			bootstrap, writer, _ := newBootstrapFixture(t, &trace, false)
			checks := 0
			bootstrap.revalidate = func() error {
				checks++
				trace = append(trace, "revalidate")
				if checks == failedCheck {
					return errors.New("authorization revoked")
				}
				return nil
			}
			if _, err := bootstrap.Run(context.Background()); err != AuthorizationFailure {
				t.Fatalf("Run error=%v, want AuthorizationFailure", err)
			}
			if writer.initialized != 0 || writer.resumed != 0 {
				t.Fatal("revoked request reached provisioner")
			}
			fetches := 0
			for _, step := range trace {
				if strings.HasPrefix(step, "fetch:") {
					fetches++
				}
			}
			if failedCheck < 3 && fetches != 0 || failedCheck == 3 && fetches != 1 {
				t.Fatalf("revocation at check %d made unexpected issuer calls: %v", failedCheck, trace)
			}
		})
	}
}

func TestFirstEnrollmentBootstrapDoesNotWriteAfterAuthorizationOrFetchFailure(t *testing.T) {
	t.Run("authorization error", func(t *testing.T) {
		trace := []string{}
		bootstrap, writer, _ := newBootstrapFixture(t, &trace, false)
		bootstrap.authorize = func() (bool, error) { trace = append(trace, "authorize"); return false, errors.New("private detail") }
		if _, err := bootstrap.Run(context.Background()); err != AuthorizationFailure {
			t.Fatalf("Run error=%v", err)
		}
		if len(trace) != 1 || writer.initialized != 0 {
			t.Fatalf("unexpected side effects: %v", trace)
		}
	})
	t.Run("fetch error", func(t *testing.T) {
		trace := []string{}
		bootstrap, writer, _ := newBootstrapFixture(t, &trace, false)
		bootstrap.fetcher.(*bootstrapFetcher).err = RecoveryRequired
		if _, err := bootstrap.Run(context.Background()); err != RecoveryRequired {
			t.Fatalf("Run error=%v", err)
		}
		if writer.initialized != 0 || writer.resumed != 0 {
			t.Fatal("issuer failure reached provisioner")
		}
	})
}
