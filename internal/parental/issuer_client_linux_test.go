package parental

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

func issuerClientForServer(t *testing.T, server *httptest.Server, verifier *SignedFirstReferenceVerifier) *SignedFirstReferenceClient {
	t.Helper()
	client, err := NewSignedFirstReferenceClient(server.URL+"/v1/first-reference", verifier)
	if err != nil {
		t.Fatal(err)
	}
	transport := server.Client().Transport
	client.http = &http.Client{
		Transport: transport,
		Timeout:   client.http.Timeout,
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
	return client
}

func issuerOuterResponse(envelope, signature []byte) []byte {
	data, _ := json.Marshal(map[string]any{
		"schema":           1,
		"envelope_base64":  base64.StdEncoding.EncodeToString(envelope),
		"signature_base64": base64.StdEncoding.EncodeToString(signature),
	})
	return data
}

func TestSignedFirstReferenceClientFetchesAndVerifiesOverHTTPS(t *testing.T) {
	home, keyring, fingerprint := issuerSigningFixture(t)
	issuer, installation, operation := strings.Repeat("a", 32), strings.Repeat("1", 32), strings.Repeat("2", 32)
	envelope := signedIssuerEnvelope(t, fingerprint, issuer, installation, operation)
	signature := signIssuerBytes(t, home, fingerprint, envelope)
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/v1/first-reference" || r.Header.Get("Content-Type") != "application/json" || r.Header.Get("Accept") != "application/json" {
			t.Errorf("unexpected request: %s %s headers=%v", r.Method, r.URL, r.Header)
		}
		var request struct {
			Schema       int    `json:"schema"`
			Installation string `json:"installation"`
			Operation    string `json:"operation"`
		}
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil || request.Schema != 1 || request.Installation != installation || request.Operation != operation {
			t.Errorf("unexpected request body: %#v, %v", request, err)
		}
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		_, _ = w.Write(issuerOuterResponse(envelope, signature))
	}))
	defer server.Close()

	verifier := NewSignedFirstReferenceVerifier(issuer, fingerprint, keyring)
	client := issuerClientForServer(t, server, verifier)
	got, err := client.Fetch(context.Background(), installation, operation)
	if err != nil || got.Issuer() != issuer || got.Reference().Installation() != installation || got.Operation() != operation {
		t.Fatalf("Fetch() = %#v, %v", got, err)
	}
}

func TestSignedFirstReferenceClientRejectsHTTPAndRedirects(t *testing.T) {
	_, keyring, fingerprint := issuerSigningFixture(t)
	verifier := NewSignedFirstReferenceVerifier(strings.Repeat("a", 32), fingerprint, keyring)
	if _, err := NewSignedFirstReferenceClient("http://issuer.invalid/v1/first-reference", verifier); err == nil {
		t.Fatal("plaintext HTTP endpoint was accepted")
	}
	var targetHits atomic.Int32
	target := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		targetHits.Add(1)
	}))
	defer target.Close()
	redirect := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Location", target.URL)
		w.WriteHeader(http.StatusTemporaryRedirect)
	}))
	defer redirect.Close()
	client := issuerClientForServer(t, redirect, verifier)
	if _, err := client.Fetch(context.Background(), strings.Repeat("1", 32), strings.Repeat("2", 32)); err == nil {
		t.Fatal("redirect response was accepted")
	}
	if targetHits.Load() != 0 {
		t.Fatal("redirect target was contacted")
	}
}

func TestSignedFirstReferenceClientRejectsBadHTTPAndOuterResponses(t *testing.T) {
	_, keyring, fingerprint := issuerSigningFixture(t)
	verifier := NewSignedFirstReferenceVerifier(strings.Repeat("a", 32), fingerprint, keyring)
	installation, operation := strings.Repeat("1", 32), strings.Repeat("2", 32)
	cases := []struct {
		name        string
		status      int
		contentType string
		body        []byte
	}{
		{name: "http status", status: http.StatusServiceUnavailable, contentType: "application/json", body: []byte(`{}`)},
		{name: "wrong media type", status: http.StatusOK, contentType: "text/plain", body: []byte(`{}`)},
		{name: "malformed json", status: http.StatusOK, contentType: "application/json", body: []byte(`{"schema":`)},
		{name: "unknown response field", status: http.StatusOK, contentType: "application/json", body: []byte(`{"schema":1,"envelope_base64":"YQ==","signature_base64":"Yg==","extra":true}`)},
		{name: "bad base64", status: http.StatusOK, contentType: "application/json", body: []byte(`{"schema":1,"envelope_base64":"!","signature_base64":"Yg=="}`)},
		{name: "oversized response", status: http.StatusOK, contentType: "application/json", body: make([]byte, SignedIssuerResponseLimit+1)},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", tc.contentType)
				w.WriteHeader(tc.status)
				_, _ = w.Write(tc.body)
			}))
			defer server.Close()
			client := issuerClientForServer(t, server, verifier)
			if _, err := client.Fetch(context.Background(), installation, operation); err == nil {
				t.Fatal("invalid issuer response was accepted")
			}
		})
	}
}

func TestSignedFirstReferenceClientRejectsInvalidTupleBeforeNetwork(t *testing.T) {
	_, keyring, fingerprint := issuerSigningFixture(t)
	verifier := NewSignedFirstReferenceVerifier(strings.Repeat("a", 32), fingerprint, keyring)
	var hits atomic.Int32
	server := httptest.NewTLSServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { hits.Add(1) }))
	defer server.Close()
	client := issuerClientForServer(t, server, verifier)
	if _, err := client.Fetch(context.Background(), "bad", strings.Repeat("2", 32)); err == nil {
		t.Fatal("invalid installation ID was accepted")
	}
	if _, err := client.Fetch(nil, strings.Repeat("1", 32), strings.Repeat("2", 32)); err == nil {
		t.Fatal("nil context was accepted")
	}
	if hits.Load() != 0 {
		t.Fatalf("invalid tuple reached issuer endpoint %d times", hits.Load())
	}
}

func TestDecodeSignedIssuerResponseRejectsTrailingAndDuplicateFields(t *testing.T) {
	for _, body := range [][]byte{
		[]byte(`{"schema":1,"envelope_base64":"YQ==","signature_base64":"Yg=="} {}`),
		[]byte(`{"schema":1,"schema":1,"envelope_base64":"YQ==","signature_base64":"Yg=="}`),
	} {
		if _, _, err := decodeSignedIssuerResponse(body); err == nil {
			t.Fatalf("invalid outer JSON accepted: %s", fmt.Sprintf("%q", body))
		}
	}
}

func TestNewSignedFirstReferenceClientRejectsUnsafeEndpointShapes(t *testing.T) {
	_, keyring, fingerprint := issuerSigningFixture(t)
	verifier := NewSignedFirstReferenceVerifier(strings.Repeat("a", 32), fingerprint, keyring)
	for _, endpoint := range []string{
		"", "https:///path", "https://user:password@issuer.invalid/v1/first-reference", "https://issuer.invalid/v1/first-reference?token=x", "https://issuer.invalid/v1/first-reference?", "https://issuer.invalid/v1/first-reference#fragment", "https://issuer.invalid/v1/first-reference#", "https://issuer.invalid/other", "https://issuer.invalid/v1%2Ffirst-reference", "https://issuer.invalid:99999/v1/first-reference", "https://issuer.invalid:/v1/first-reference",
	} {
		if _, err := NewSignedFirstReferenceClient(endpoint, verifier); err == nil {
			t.Errorf("unsafe endpoint %q was accepted", endpoint)
		}
	}
	if _, err := NewSignedFirstReferenceClient("https://issuer.invalid/path", nil); err == nil {
		t.Fatal("nil signature verifier was accepted")
	}
}
