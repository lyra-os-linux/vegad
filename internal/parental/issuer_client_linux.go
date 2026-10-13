package parental

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/base64"
	"encoding/json"
	"io"
	"mime"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

const SignedIssuerResponseLimit = 96*1024 + 32*1024

// SignedFirstReferenceClient implements the bounded HTTPS transport contract.
// The endpoint is trusted service configuration, never a D-Bus argument.
// Response authenticity still comes from SignedFirstReferenceVerifier.
type SignedFirstReferenceClient struct {
	endpoint string
	verifier *SignedFirstReferenceVerifier
	http     *http.Client
}

func (c *SignedFirstReferenceClient) IssuerIdentity() (string, string) {
	if c == nil || c.verifier == nil {
		return "", ""
	}
	return c.verifier.issuer, c.verifier.fingerprint
}

func NewSignedFirstReferenceClient(endpoint string, verifier *SignedFirstReferenceVerifier) (*SignedFirstReferenceClient, error) {
	u, err := url.Parse(endpoint)
	if err != nil || verifier == nil || u.Scheme != "https" || u.Hostname() == "" || u.Path != "/v1/first-reference" || u.RawPath != "" || u.User != nil || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" || u.RawFragment != "" || u.Opaque != "" || strings.ContainsAny(endpoint, "?#") || strings.HasSuffix(u.Host, ":") {
		return nil, RecoveryRequired
	}
	if port := u.Port(); port != "" {
		n, parseErr := strconv.Atoi(port)
		if parseErr != nil || n < 1 || n > 65535 {
			return nil, RecoveryRequired
		}
	}
	defaultTransport, ok := http.DefaultTransport.(*http.Transport)
	if !ok {
		return nil, RecoveryRequired
	}
	transport := defaultTransport.Clone()
	transport.TLSClientConfig = &tls.Config{MinVersion: tls.VersionTLS12}
	return &SignedFirstReferenceClient{
		endpoint: endpoint,
		verifier: verifier,
		http: &http.Client{
			Transport: transport,
			Timeout:   15 * time.Second,
			CheckRedirect: func(*http.Request, []*http.Request) error {
				return http.ErrUseLastResponse
			},
		},
	}, nil
}

// Fetch requests only the tuple already authorized by the service's local ESP
// permission reader, then verifies the detached issuer signature and every
// tuple field before returning the immutable reference.
func (c *SignedFirstReferenceClient) Fetch(ctx context.Context, installation, operation string) (SignedFirstReference, error) {
	if c == nil || c.verifier == nil || c.http == nil || !token.MatchString(installation) || !token.MatchString(operation) {
		return SignedFirstReference{}, Denied
	}
	requestBody, err := json.Marshal(map[string]any{"schema": 1, "installation": installation, "operation": operation})
	if err != nil {
		return SignedFirstReference{}, RecoveryRequired
	}
	if ctx == nil {
		return SignedFirstReference{}, Denied
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, c.endpoint, bytes.NewReader(requestBody))
	if err != nil {
		return SignedFirstReference{}, RecoveryRequired
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Accept", "application/json")
	response, err := c.http.Do(request)
	if err != nil {
		return SignedFirstReference{}, RecoveryRequired
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return SignedFirstReference{}, RecoveryRequired
	}
	mediaType, _, err := mime.ParseMediaType(response.Header.Get("Content-Type"))
	if err != nil || mediaType != "application/json" {
		return SignedFirstReference{}, InvalidReference
	}
	body, err := io.ReadAll(io.LimitReader(response.Body, SignedIssuerResponseLimit+1))
	if err != nil || len(body) == 0 || len(body) > SignedIssuerResponseLimit || !utf8.Valid(body) {
		return SignedFirstReference{}, InvalidReference
	}
	envelope, signature, err := decodeSignedIssuerResponse(body)
	if err != nil {
		return SignedFirstReference{}, err
	}
	return c.verifier.Verify(envelope, signature, installation, operation)
}

func decodeSignedIssuerResponse(data []byte) ([]byte, []byte, error) {
	if len(data) == 0 || len(data) > SignedIssuerResponseLimit || !utf8.Valid(data) {
		return nil, nil, InvalidReference
	}
	d := json.NewDecoder(bytes.NewReader(data))
	d.UseNumber()
	raw, err := readJSON(d, 0)
	if err != nil {
		return nil, nil, InvalidReference
	}
	if _, err = d.Token(); err != io.EOF {
		return nil, nil, InvalidReference
	}
	row, ok := object(raw, "schema", "envelope_base64", "signature_base64")
	if !ok {
		return nil, nil, InvalidReference
	}
	if schema, valid := positive(row["schema"], 1); !valid || schema != 1 {
		return nil, nil, InvalidReference
	}
	envelopeText, valid := row["envelope_base64"].(string)
	if !valid || len(envelopeText) > base64.StdEncoding.EncodedLen(SignedIssuerEnvelopeLimit) {
		return nil, nil, InvalidReference
	}
	signatureText, valid := row["signature_base64"].(string)
	if !valid || len(signatureText) > base64.StdEncoding.EncodedLen(SignedIssuerSignatureLimit) {
		return nil, nil, InvalidReference
	}
	envelope, err := base64.StdEncoding.Strict().DecodeString(envelopeText)
	if err != nil || len(envelope) == 0 || len(envelope) > SignedIssuerEnvelopeLimit || base64.StdEncoding.EncodeToString(envelope) != envelopeText {
		return nil, nil, InvalidReference
	}
	signature, err := base64.StdEncoding.Strict().DecodeString(signatureText)
	if err != nil || len(signature) == 0 || len(signature) > SignedIssuerSignatureLimit || base64.StdEncoding.EncodeToString(signature) != signatureText {
		return nil, nil, InvalidReference
	}
	return envelope, signature, nil
}
