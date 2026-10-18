package pki

import (
	"crypto/ecdsa"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"runtime"
	"sync/atomic"
)

func isWindows() bool { return runtime.GOOS == "windows" }

// KeyPair holds a certificate that can be swapped atomically while TLS
// listeners and clients keep using it.
type KeyPair struct {
	cur atomic.Pointer[tls.Certificate]
}

// NewKeyPair builds a KeyPair from a PEM certificate and its private key.
func NewKeyPair(certPEM []byte, key *ecdsa.PrivateKey) (*KeyPair, error) {
	kp := &KeyPair{}
	if err := kp.Set(certPEM, key); err != nil {
		return nil, err
	}
	return kp, nil
}

// Set replaces the current certificate after checking it matches key.
func (kp *KeyPair) Set(certPEM []byte, key *ecdsa.PrivateKey) error {
	leaf, err := ParseCert(certPEM)
	if err != nil {
		return err
	}
	if !key.PublicKey.Equal(leaf.PublicKey) {
		return errors.New("certificate does not match private key")
	}
	kp.cur.Store(&tls.Certificate{Certificate: [][]byte{leaf.Raw}, PrivateKey: key, Leaf: leaf})
	return nil
}

// Leaf returns the current leaf certificate.
func (kp *KeyPair) Leaf() *x509.Certificate { return kp.cur.Load().Leaf }

func (kp *KeyPair) getCertificate(*tls.ClientHelloInfo) (*tls.Certificate, error) {
	return kp.cur.Load(), nil
}

func (kp *KeyPair) getClientCertificate(*tls.CertificateRequestInfo) (*tls.Certificate, error) {
	return kp.cur.Load(), nil
}

// AgentServerConfig is the TLS configuration of an agent's gRPC listener:
// TLS 1.3 only, a client certificate from the CA is mandatory and it must
// identify the hub.
func AgentServerConfig(kp *KeyPair, caPool *x509.CertPool) *tls.Config {
	return &tls.Config{
		MinVersion:     tls.VersionTLS13,
		GetCertificate: kp.getCertificate,
		ClientAuth:     tls.RequireAndVerifyClientCert,
		ClientCAs:      caPool,
		VerifyConnection: func(cs tls.ConnectionState) error {
			if len(cs.PeerCertificates) == 0 {
				return errors.New("no client certificate")
			}
			if err := cs.PeerCertificates[0].VerifyHostname(HubName); err != nil {
				return fmt.Errorf("client is not the hub: %w", err)
			}
			return nil
		},
		NextProtos: []string{"h2"},
	}
}

// HubClientConfig is the TLS configuration the hub uses to dial an agent.
// Besides chain and name verification, pin is called with the fingerprint
// of the agent's leaf so the hub only accepts certificates it issued to that
// agent and has not revoked.
func HubClientConfig(kp *KeyPair, caPool *x509.CertPool, agentID string, pin func(fingerprint string) error) *tls.Config {
	return &tls.Config{
		MinVersion:           tls.VersionTLS13,
		RootCAs:              caPool,
		ServerName:           AgentName(agentID),
		GetClientCertificate: kp.getClientCertificate,
		VerifyConnection: func(cs tls.ConnectionState) error {
			if len(cs.PeerCertificates) == 0 {
				return errors.New("no server certificate")
			}
			if pin == nil {
				return nil
			}
			return pin(Fingerprint(cs.PeerCertificates[0]))
		},
		NextProtos: []string{"h2"},
	}
}

// ServerConfig is a plain TLS 1.3 server configuration (enrollment endpoint).
func ServerConfig(kp *KeyPair) *tls.Config {
	return &tls.Config{
		MinVersion:     tls.VersionTLS13,
		GetCertificate: kp.getCertificate,
	}
}

// PinnedCAClientConfig is used by an agent during enrollment, before it has
// the CA certificate. It accepts the server only if the presented chain
// contains a CA whose fingerprint equals caFingerprint, the leaf is signed by
// it and the leaf identifies the hub. On success the CA is passed to onCA.
func PinnedCAClientConfig(caFingerprint string, onCA func(*x509.Certificate)) *tls.Config {
	want := NormalizeFingerprint(caFingerprint)
	return &tls.Config{
		MinVersion: tls.VersionTLS13,
		// Standard verification is replaced by the pinned check below, which
		// performs full chain and hostname verification against the pinned CA.
		InsecureSkipVerify: true,
		VerifyConnection: func(cs tls.ConnectionState) error {
			if len(cs.PeerCertificates) < 2 {
				return errors.New("server did not present its CA certificate")
			}
			var ca *x509.Certificate
			for _, c := range cs.PeerCertificates[1:] {
				if Fingerprint(c) == want {
					ca = c
					break
				}
			}
			if ca == nil || !ca.IsCA {
				return errors.New("hub CA fingerprint mismatch")
			}
			pool := x509.NewCertPool()
			pool.AddCert(ca)
			if _, err := cs.PeerCertificates[0].Verify(x509.VerifyOptions{
				DNSName:   HubName,
				Roots:     pool,
				KeyUsages: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
			}); err != nil {
				return fmt.Errorf("hub certificate: %w", err)
			}
			if onCA != nil {
				onCA(ca)
			}
			return nil
		},
	}
}

// WithChain returns a GetCertificate callback whose certificate also carries the CA
// certificate, as needed by PinnedCAClientConfig on the other side.
func (kp *KeyPair) WithChain(ca *x509.Certificate) func(*tls.ClientHelloInfo) (*tls.Certificate, error) {
	return func(*tls.ClientHelloInfo) (*tls.Certificate, error) {
		c := *kp.cur.Load()
		c.Certificate = append([][]byte{c.Certificate[0]}, ca.Raw)
		return &c, nil
	}
}
