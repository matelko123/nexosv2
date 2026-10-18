// Package enroll defines the one-time agent enrollment exchange.
//
// The hub creates an enrollment bundle (hub address, CA fingerprint, agent id
// and a single-use secret). The agent generates its key locally, connects to
// the hub enrollment endpoint over TLS pinned to the CA fingerprint and sends
// a CSR together with the secret. The hub answers with the signed agent
// certificate and the CA certificate.
package enroll

import (
	"bytes"
	"context"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"nexos/internal/pki"
)

const (
	bundlePrefix = "nxe1_"
	Path         = "/v1/enroll"
	maxBody      = 64 << 10
)

// Bundle is everything an agent needs to enroll.
type Bundle struct {
	HubAddr       string `json:"hub"`
	CAFingerprint string `json:"ca"`
	AgentID       string `json:"id"`
	Secret        string `json:"secret"`
}

// Encode returns the single copy-paste string representation of b.
func (b Bundle) Encode() string {
	raw, _ := json.Marshal(b)
	return bundlePrefix + base64.RawURLEncoding.EncodeToString(raw)
}

// ParseBundle decodes a string produced by Bundle.Encode.
func ParseBundle(s string) (Bundle, error) {
	var b Bundle
	// Tolerate surrounding text, e.g. a pasted "NEXOS_ENROLL=nxe1_..." line
	// or the whole .env snippet: the bundle is the token starting with the prefix.
	i := strings.Index(s, bundlePrefix)
	if i < 0 {
		return b, errors.New("not a NexOS enrollment bundle (expected a value starting with " + bundlePrefix + ")")
	}
	s = s[i:]
	if end := strings.IndexAny(s, " \t\r\n'\""); end >= 0 {
		s = s[:end]
	}
	raw, err := base64.RawURLEncoding.DecodeString(strings.TrimPrefix(s, bundlePrefix))
	if err != nil {
		return b, fmt.Errorf("decode bundle: %w", err)
	}
	if err := json.Unmarshal(raw, &b); err != nil {
		return b, fmt.Errorf("decode bundle: %w", err)
	}
	if b.HubAddr == "" || b.CAFingerprint == "" || b.Secret == "" || !pki.ValidAgentID(b.AgentID) {
		return b, errors.New("incomplete enrollment bundle")
	}
	return b, nil
}

type Request struct {
	AgentID  string `json:"agent_id"`
	Secret   string `json:"secret"`
	CSRPEM   string `json:"csr_pem"`
	Hostname string `json:"hostname"`
}

type Response struct {
	CertificatePEM string `json:"certificate_pem"`
	CAPEM          string `json:"ca_pem"`
}

// Result is what a successful enrollment yields to the agent.
type Result struct {
	CertPEM []byte
	CAPEM   []byte
}

// Do performs the enrollment from the agent side.
func Do(ctx context.Context, b Bundle, csrPEM []byte, hostname string) (*Result, error) {
	var pinnedCA *x509.Certificate
	client := &http.Client{
		Timeout: 30 * time.Second,
		Transport: &http.Transport{
			TLSClientConfig:   pki.PinnedCAClientConfig(b.CAFingerprint, func(c *x509.Certificate) { pinnedCA = c }),
			ForceAttemptHTTP2: true,
		},
	}
	body, _ := json.Marshal(Request{AgentID: b.AgentID, Secret: b.Secret, CSRPEM: string(csrPEM), Hostname: hostname})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, "https://"+b.HubAddr+Path, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxBody))
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("hub rejected enrollment: %s: %s", resp.Status, strings.TrimSpace(string(data)))
	}
	var out Response
	if err := json.Unmarshal(data, &out); err != nil {
		return nil, err
	}
	ca, err := pki.ParseCert([]byte(out.CAPEM))
	if err != nil {
		return nil, fmt.Errorf("ca: %w", err)
	}
	if pinnedCA == nil || !ca.Equal(pinnedCA) {
		return nil, errors.New("returned CA differs from pinned CA")
	}
	leaf, err := pki.ParseCert([]byte(out.CertificatePEM))
	if err != nil {
		return nil, fmt.Errorf("certificate: %w", err)
	}
	pool := x509.NewCertPool()
	pool.AddCert(ca)
	if _, err := leaf.Verify(x509.VerifyOptions{
		DNSName:   pki.AgentName(b.AgentID),
		Roots:     pool,
		KeyUsages: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}); err != nil {
		return nil, fmt.Errorf("issued certificate invalid: %w", err)
	}
	return &Result{CertPEM: []byte(out.CertificatePEM), CAPEM: []byte(out.CAPEM)}, nil
}

// DecodeRequest reads and size-limits an enrollment request body.
func DecodeRequest(r io.Reader) (Request, error) {
	var req Request
	dec := json.NewDecoder(io.LimitReader(r, maxBody))
	dec.DisallowUnknownFields()
	err := dec.Decode(&req)
	return req, err
}
