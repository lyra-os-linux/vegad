package parental

import "context"

// EnrollmentOperationSource reads the root-managed, ESP-backed operation for
// the running installation. The consumed bit selects exact transaction resume;
// it does not establish freshness by itself.
type EnrollmentOperationSource interface {
	CurrentEnrollment() (installation, operation string, consumed bool, err error)
}

type FirstReferenceFetcher interface {
	IssuerIdentity() (issuer, fingerprint string)
	Fetch(context.Context, string, string) (SignedFirstReference, error)
}

type InitialReferenceProvisioner interface {
	Issuer() string
	Initialize(RecoveryReference, string, func() error) (RecoveryReference, error)
	ResumeInitial(RecoveryReference, string, func() error) (RecoveryReference, error)
}

// FirstEnrollmentBootstrap composes authorization, trusted local permit lookup,
// signed issuer retrieval and the crash-resumable first-reference writer. It is
// not a D-Bus service and is not enabled by vegad until its production
// dependencies, maintenance authorization and storage topology are qualified.
type FirstEnrollmentBootstrap struct {
	issuer      string
	fingerprint string
	permission  EnrollmentOperationSource
	fetcher     FirstReferenceFetcher
	writer      InitialReferenceProvisioner
	authorize   func() (bool, error)
	revalidate  func() error
}

func NewFirstEnrollmentBootstrap(issuer, fingerprint string, permission EnrollmentOperationSource, fetcher FirstReferenceFetcher, writer InitialReferenceProvisioner, authorize func() (bool, error), revalidate func() error) *FirstEnrollmentBootstrap {
	return &FirstEnrollmentBootstrap{issuer: issuer, fingerprint: fingerprint, permission: permission, fetcher: fetcher, writer: writer, authorize: authorize, revalidate: revalidate}
}

// Run performs interactive authorization before reading installer state or
// contacting the issuer. The writer invokes revalidate at its authorization
// boundaries, before storage access, during publication and after consuming
// the one-use permission. authorize must not hold a parental storage lease;
// revalidate must be a fresh, noninteractive caller/session/Polkit check.
func (b *FirstEnrollmentBootstrap) Run(ctx context.Context) (RecoveryReference, error) {
	if b == nil || !token.MatchString(b.issuer) || !issuerFingerprint.MatchString(b.fingerprint) || b.permission == nil || b.fetcher == nil || b.writer == nil || b.authorize == nil || b.revalidate == nil || ctx == nil {
		return RecoveryReference{}, Denied
	}
	if err := ctx.Err(); err != nil {
		return RecoveryReference{}, err
	}
	configuredIssuer, configuredFingerprint := b.fetcher.IssuerIdentity()
	if configuredIssuer != b.issuer || configuredFingerprint != b.fingerprint || b.writer.Issuer() != b.issuer {
		return RecoveryReference{}, Denied
	}
	allowed, err := b.authorize()
	if err != nil {
		return RecoveryReference{}, AuthorizationFailure
	}
	if !allowed {
		return RecoveryReference{}, Denied
	}
	if err = b.revalidate(); err != nil {
		return RecoveryReference{}, AuthorizationFailure
	}
	installation, operation, consumed, err := b.permission.CurrentEnrollment()
	if err != nil {
		return RecoveryReference{}, err
	}
	if !token.MatchString(installation) || !token.MatchString(operation) {
		return RecoveryReference{}, Denied
	}
	if err = b.revalidate(); err != nil {
		return RecoveryReference{}, AuthorizationFailure
	}
	issued, err := b.fetcher.Fetch(ctx, installation, operation)
	if err != nil {
		return RecoveryReference{}, err
	}
	if issued.Issuer() != b.issuer || issued.Fingerprint() != b.fingerprint ||
		issued.Reference().Installation() != installation || issued.Operation() != operation ||
		issued.Reference().Mode() != "first-install" || issued.Reference().Sequence() != 1 || issued.Reference().Catalog().Revision() != 1 {
		return RecoveryReference{}, InvalidReference
	}
	if err = b.revalidate(); err != nil {
		return RecoveryReference{}, AuthorizationFailure
	}
	if consumed {
		return b.writer.ResumeInitial(issued.Reference(), operation, b.revalidate)
	}
	return b.writer.Initialize(issued.Reference(), operation, b.revalidate)
}
