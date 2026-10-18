// Package identity manages the agent's key, certificate and the hub CA on
// disk, plus the pending key used during certificate rotation.
package identity

import (
	"crypto/ecdsa"
	"crypto/x509"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"

	"nexos/internal/pki"
)

const (
	keyFile  = "agent.key"
	certFile = "agent.crt"
	caFile   = "ca.crt"
	idFile   = "agent.id"
)

// Identity is a loaded, enrolled agent identity.
type Identity struct {
	dir     string
	AgentID string
	CA      *x509.Certificate
	CAPool  *x509.CertPool
	KeyPair *pki.KeyPair

	mu      sync.Mutex
	pending *ecdsa.PrivateKey
}

// Exists reports whether dir already contains an enrolled identity.
func Exists(dir string) bool {
	_, err := os.Stat(filepath.Join(dir, certFile))
	return err == nil
}

// Save writes a freshly enrolled identity to dir.
func Save(dir, agentID string, key *ecdsa.PrivateKey, certPEM, caPEM []byte) error {
	keyPEM, err := pki.EncodeKey(key)
	if err != nil {
		return err
	}
	if err := pki.WriteFileAtomic(filepath.Join(dir, keyFile), keyPEM, 0o600); err != nil {
		return err
	}
	if err := pki.WriteFileAtomic(filepath.Join(dir, caFile), caPEM, 0o644); err != nil {
		return err
	}
	if err := pki.WriteFileAtomic(filepath.Join(dir, idFile), []byte(agentID+"\n"), 0o644); err != nil {
		return err
	}
	// Certificate last: its presence marks enrollment as complete.
	return pki.WriteFileAtomic(filepath.Join(dir, certFile), certPEM, 0o644)
}

// recoverRotation completes or discards a rotation interrupted by a crash.
// Install writes key.new then cert.new, then renames key, then cert.
func recoverRotation(dir string) error {
	p := func(name string) string { return filepath.Join(dir, name) }
	exists := func(name string) bool { _, err := os.Stat(p(name)); return err == nil }
	switch {
	case exists(certFile + ".new"):
		if exists(keyFile + ".new") {
			if err := os.Rename(p(keyFile+".new"), p(keyFile)); err != nil {
				return err
			}
		}
		return os.Rename(p(certFile+".new"), p(certFile))
	case exists(keyFile + ".new"):
		return os.Remove(p(keyFile + ".new"))
	}
	return nil
}

// Load reads an enrolled identity from dir and validates it.
func Load(dir string) (*Identity, error) {
	if err := recoverRotation(dir); err != nil {
		return nil, fmt.Errorf("recover interrupted rotation: %w", err)
	}
	read := func(name string) ([]byte, error) {
		b, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			return nil, fmt.Errorf("read %s: %w", name, err)
		}
		return b, nil
	}
	idRaw, err := read(idFile)
	if err != nil {
		return nil, err
	}
	agentID := string(trimNL(idRaw))
	if !pki.ValidAgentID(agentID) {
		return nil, errors.New("invalid agent id on disk")
	}
	keyPEM, err := read(keyFile)
	if err != nil {
		return nil, err
	}
	key, err := pki.ParseKey(keyPEM)
	if err != nil {
		return nil, err
	}
	certPEM, err := read(certFile)
	if err != nil {
		return nil, err
	}
	caPEM, err := read(caFile)
	if err != nil {
		return nil, err
	}
	ca, err := pki.ParseCert(caPEM)
	if err != nil {
		return nil, err
	}
	kp, err := pki.NewKeyPair(certPEM, key)
	if err != nil {
		return nil, err
	}
	pool := x509.NewCertPool()
	pool.AddCert(ca)
	id := &Identity{dir: dir, AgentID: agentID, CA: ca, CAPool: pool, KeyPair: kp}
	if err := id.verifyLeaf(kp.Leaf()); err != nil {
		return nil, err
	}
	return id, nil
}

func trimNL(b []byte) []byte {
	for len(b) > 0 && (b[len(b)-1] == '\n' || b[len(b)-1] == '\r' || b[len(b)-1] == ' ') {
		b = b[:len(b)-1]
	}
	return b
}

func (id *Identity) verifyLeaf(leaf *x509.Certificate) error {
	_, err := leaf.Verify(x509.VerifyOptions{
		DNSName:   pki.AgentName(id.AgentID),
		Roots:     id.CAPool,
		KeyUsages: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	})
	if err != nil {
		return fmt.Errorf("agent certificate: %w", err)
	}
	return nil
}

// NewCSR generates a pending key and returns a CSR for it. A later call
// replaces any previous pending key.
func (id *Identity) NewCSR() ([]byte, error) {
	key, err := pki.GenerateKey()
	if err != nil {
		return nil, err
	}
	csr, err := pki.CreateCSR(key, pki.AgentName(id.AgentID))
	if err != nil {
		return nil, err
	}
	id.mu.Lock()
	id.pending = key
	id.mu.Unlock()
	return csr, nil
}

// Install validates certPEM against the pending key and the CA, persists it
// and swaps it into the live TLS configuration.
func (id *Identity) Install(certPEM []byte) (time.Time, error) {
	id.mu.Lock()
	defer id.mu.Unlock()
	if id.pending == nil {
		return time.Time{}, errors.New("no pending certificate request")
	}
	leaf, err := pki.ParseCert(certPEM)
	if err != nil {
		return time.Time{}, err
	}
	if !id.pending.PublicKey.Equal(leaf.PublicKey) {
		return time.Time{}, errors.New("certificate does not match pending key")
	}
	if err := id.verifyLeaf(leaf); err != nil {
		return time.Time{}, err
	}
	keyPEM, err := pki.EncodeKey(id.pending)
	if err != nil {
		return time.Time{}, err
	}
	// Write key and cert under new names first, then move both into place,
	// so a crash cannot leave a mismatched pair on disk.
	if err := pki.WriteFileAtomic(filepath.Join(id.dir, keyFile+".new"), keyPEM, 0o600); err != nil {
		return time.Time{}, err
	}
	if err := pki.WriteFileAtomic(filepath.Join(id.dir, certFile+".new"), certPEM, 0o644); err != nil {
		return time.Time{}, err
	}
	if err := os.Rename(filepath.Join(id.dir, keyFile+".new"), filepath.Join(id.dir, keyFile)); err != nil {
		return time.Time{}, err
	}
	if err := os.Rename(filepath.Join(id.dir, certFile+".new"), filepath.Join(id.dir, certFile)); err != nil {
		return time.Time{}, err
	}
	if err := id.KeyPair.Set(certPEM, id.pending); err != nil {
		return time.Time{}, err
	}
	id.pending = nil
	return leaf.NotAfter, nil
}
