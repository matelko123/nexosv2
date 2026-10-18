// Package pki implements the private certificate authority used for mutual
// TLS between the hub and its agents.
//
// Identities are carried as DNS SANs in a private namespace:
//
//	hub.nexos            – the hub (client auth towards agents, server auth for enrollment/UI)
//	<id>.agent.nexos     – an agent (server auth towards the hub)
//
// Using DNS SANs lets the standard Go verifier check identities through
// tls.Config.ServerName, without custom chain verification.
package pki

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/hex"
	"encoding/pem"
	"errors"
	"fmt"
	"math/big"
	"net"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

const (
	HubName         = "hub.nexos"
	agentNameSuffix = ".agent.nexos"

	CAValidity   = 10 * 365 * 24 * time.Hour
	LeafValidity = 30 * 24 * time.Hour
	// RenewBefore is how long before expiry a leaf certificate is rotated.
	RenewBefore = 10 * 24 * time.Hour

	clockSkew = 5 * time.Minute
)

var agentIDPattern = regexp.MustCompile(`^[a-z0-9]{8,32}$`)

// AgentName returns the DNS SAN identifying the agent with the given id.
func AgentName(id string) string { return id + agentNameSuffix }

// ValidAgentID reports whether id is a well-formed agent identifier.
func ValidAgentID(id string) bool { return agentIDPattern.MatchString(id) }

// NewAgentID returns a random agent identifier.
func NewAgentID() string {
	const alphabet = "abcdefghijklmnopqrstuvwxyz0123456789"
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	for i := range b {
		b[i] = alphabet[int(b[i])%len(alphabet)]
	}
	return string(b)
}

// CA is a certificate authority with its private key.
type CA struct {
	Cert    *x509.Certificate
	CertPEM []byte
	Key     *ecdsa.PrivateKey
}

// GenerateKey creates a new ECDSA P-256 private key.
func GenerateKey() (*ecdsa.PrivateKey, error) {
	return ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
}

func randomSerial() (*big.Int, error) {
	return rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 127))
}

// NewCA creates a fresh self-signed certificate authority.
func NewCA(commonName string) (*CA, error) {
	key, err := GenerateKey()
	if err != nil {
		return nil, err
	}
	serial, err := randomSerial()
	if err != nil {
		return nil, err
	}
	now := time.Now()
	tmpl := &x509.Certificate{
		SerialNumber:          serial,
		Subject:               pkix.Name{CommonName: commonName, Organization: []string{"NexOS"}},
		NotBefore:             now.Add(-clockSkew),
		NotAfter:              now.Add(CAValidity),
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageCRLSign,
		BasicConstraintsValid: true,
		IsCA:                  true,
		MaxPathLenZero:        true,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		return nil, err
	}
	cert, err := x509.ParseCertificate(der)
	if err != nil {
		return nil, err
	}
	return &CA{Cert: cert, CertPEM: EncodeCert(der), Key: key}, nil
}

// LoadOrCreateCA loads ca.crt/ca.key from dir, creating them if absent.
func LoadOrCreateCA(dir string) (*CA, error) {
	certPath, keyPath := filepath.Join(dir, "ca.crt"), filepath.Join(dir, "ca.key")
	certPEM, certErr := os.ReadFile(certPath)
	keyPEM, keyErr := os.ReadFile(keyPath)
	if errors.Is(certErr, os.ErrNotExist) && errors.Is(keyErr, os.ErrNotExist) {
		ca, err := NewCA("NexOS Root CA")
		if err != nil {
			return nil, err
		}
		keyPEM, err := EncodeKey(ca.Key)
		if err != nil {
			return nil, err
		}
		if err := WriteFileAtomic(keyPath, keyPEM, 0o600); err != nil {
			return nil, err
		}
		if err := WriteFileAtomic(certPath, ca.CertPEM, 0o644); err != nil {
			return nil, err
		}
		return ca, nil
	}
	if certErr != nil {
		return nil, certErr
	}
	if keyErr != nil {
		return nil, keyErr
	}
	cert, err := ParseCert(certPEM)
	if err != nil {
		return nil, fmt.Errorf("ca.crt: %w", err)
	}
	key, err := ParseKey(keyPEM)
	if err != nil {
		return nil, fmt.Errorf("ca.key: %w", err)
	}
	if !cert.IsCA {
		return nil, errors.New("ca.crt is not a CA certificate")
	}
	if !key.PublicKey.Equal(cert.PublicKey) {
		return nil, errors.New("ca.key does not match ca.crt")
	}
	return &CA{Cert: cert, CertPEM: certPEM, Key: key}, nil
}

// Pool returns a cert pool containing only this CA.
func (ca *CA) Pool() *x509.CertPool {
	p := x509.NewCertPool()
	p.AddCert(ca.Cert)
	return p
}

// LeafOptions describes a leaf certificate to issue.
type LeafOptions struct {
	CommonName  string
	DNSNames    []string
	IPs         []net.IP
	ExtKeyUsage []x509.ExtKeyUsage
	Validity    time.Duration
}

// Issue signs a leaf certificate for pub. Subject and SANs come exclusively
// from opts; nothing requested by the key holder is trusted.
func (ca *CA) Issue(pub *ecdsa.PublicKey, opts LeafOptions) (*x509.Certificate, []byte, error) {
	if opts.Validity <= 0 {
		opts.Validity = LeafValidity
	}
	serial, err := randomSerial()
	if err != nil {
		return nil, nil, err
	}
	now := time.Now()
	notAfter := now.Add(opts.Validity)
	if notAfter.After(ca.Cert.NotAfter) {
		notAfter = ca.Cert.NotAfter
	}
	tmpl := &x509.Certificate{
		SerialNumber:          serial,
		Subject:               pkix.Name{CommonName: opts.CommonName, Organization: []string{"NexOS"}},
		DNSNames:              opts.DNSNames,
		IPAddresses:           opts.IPs,
		NotBefore:             now.Add(-clockSkew),
		NotAfter:              notAfter,
		KeyUsage:              x509.KeyUsageDigitalSignature,
		ExtKeyUsage:           opts.ExtKeyUsage,
		BasicConstraintsValid: true,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, ca.Cert, pub, ca.Key)
	if err != nil {
		return nil, nil, err
	}
	cert, err := x509.ParseCertificate(der)
	if err != nil {
		return nil, nil, err
	}
	return cert, EncodeCert(der), nil
}

// SignAgentCSR verifies the CSR signature and issues an agent certificate
// bound to agentID. Only the public key is taken from the CSR.
func (ca *CA) SignAgentCSR(csrPEM []byte, agentID string) (*x509.Certificate, []byte, error) {
	if !ValidAgentID(agentID) {
		return nil, nil, errors.New("invalid agent id")
	}
	pub, err := ParseCSR(csrPEM)
	if err != nil {
		return nil, nil, err
	}
	return ca.Issue(pub, LeafOptions{
		CommonName:  AgentName(agentID),
		DNSNames:    []string{AgentName(agentID)},
		ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	})
}

// CreateCSR builds a PEM encoded certificate signing request for key.
func CreateCSR(key *ecdsa.PrivateKey, commonName string) ([]byte, error) {
	der, err := x509.CreateCertificateRequest(rand.Reader, &x509.CertificateRequest{
		Subject: pkix.Name{CommonName: commonName},
	}, key)
	if err != nil {
		return nil, err
	}
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE REQUEST", Bytes: der}), nil
}

// ParseCSR decodes a CSR, checks its self-signature and returns its P-256 key.
func ParseCSR(csrPEM []byte) (*ecdsa.PublicKey, error) {
	block, _ := pem.Decode(csrPEM)
	if block == nil || block.Type != "CERTIFICATE REQUEST" {
		return nil, errors.New("invalid CSR PEM")
	}
	csr, err := x509.ParseCertificateRequest(block.Bytes)
	if err != nil {
		return nil, err
	}
	if err := csr.CheckSignature(); err != nil {
		return nil, fmt.Errorf("CSR signature: %w", err)
	}
	pub, ok := csr.PublicKey.(*ecdsa.PublicKey)
	if !ok || pub.Curve != elliptic.P256() {
		return nil, errors.New("CSR key must be ECDSA P-256")
	}
	return pub, nil
}

// Fingerprint returns "sha256:<hex>" of the certificate's DER encoding.
func Fingerprint(cert *x509.Certificate) string {
	sum := sha256.Sum256(cert.Raw)
	return "sha256:" + hex.EncodeToString(sum[:])
}

// NormalizeFingerprint lowercases a fingerprint and strips separators.
func NormalizeFingerprint(fp string) string {
	fp = strings.ToLower(strings.TrimSpace(fp))
	fp = strings.TrimPrefix(fp, "sha256:")
	fp = strings.ReplaceAll(fp, ":", "")
	return "sha256:" + fp
}

func EncodeCert(der []byte) []byte {
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
}

func EncodeKey(key *ecdsa.PrivateKey) ([]byte, error) {
	der, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		return nil, err
	}
	return pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der}), nil
}

// ParseCert decodes the first certificate in a PEM bundle.
func ParseCert(certPEM []byte) (*x509.Certificate, error) {
	block, _ := pem.Decode(certPEM)
	if block == nil || block.Type != "CERTIFICATE" {
		return nil, errors.New("invalid certificate PEM")
	}
	return x509.ParseCertificate(block.Bytes)
}

func ParseKey(keyPEM []byte) (*ecdsa.PrivateKey, error) {
	block, _ := pem.Decode(keyPEM)
	if block == nil || block.Type != "PRIVATE KEY" {
		return nil, errors.New("invalid private key PEM")
	}
	k, err := x509.ParsePKCS8PrivateKey(block.Bytes)
	if err != nil {
		return nil, err
	}
	ek, ok := k.(*ecdsa.PrivateKey)
	if !ok {
		return nil, errors.New("private key is not ECDSA")
	}
	return ek, nil
}

// WriteFileAtomic writes data to a temp file in the same directory and
// renames it over path, so readers never observe a partial file.
func WriteFileAtomic(path string, data []byte, perm os.FileMode) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	f, err := os.CreateTemp(dir, ".tmp-"+filepath.Base(path)+"-*")
	if err != nil {
		return err
	}
	tmp := f.Name()
	defer os.Remove(tmp)
	if _, err := f.Write(data); err != nil {
		f.Close()
		return err
	}
	if err := f.Chmod(perm); err != nil && !isWindows() {
		f.Close()
		return err
	}
	if err := f.Sync(); err != nil {
		f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}
