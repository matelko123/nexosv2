// Package certs manages the hub's own certificates: the agent-facing mTLS
// identity and the browser-facing UI certificate. Both are kept short-lived
// and renewed automatically.
package certs

import (
	"context"
	"crypto/x509"
	"errors"
	"log/slog"
	"net"
	"os"
	"path/filepath"
	"slices"
	"time"

	"nexos/internal/pki"
)

// Leaf is a renewable leaf certificate stored as <name>.crt / <name>.key.
type Leaf struct {
	dir  string
	name string
	ca   *pki.CA
	opts pki.LeafOptions
	KP   *pki.KeyPair
}

// LoadLeaf loads or issues the leaf, re-issuing when it is close to expiry,
// signed by another CA, or its SANs differ from opts.
func LoadLeaf(dir, name string, ca *pki.CA, opts pki.LeafOptions) (*Leaf, error) {
	l := &Leaf{dir: dir, name: name, ca: ca, opts: opts}
	certPEM, err1 := os.ReadFile(filepath.Join(dir, name+".crt"))
	keyPEM, err2 := os.ReadFile(filepath.Join(dir, name+".key"))
	if err1 == nil && err2 == nil {
		key, kerr := pki.ParseKey(keyPEM)
		if kerr == nil {
			if kp, err := pki.NewKeyPair(certPEM, key); err == nil && l.fresh(kp.Leaf()) {
				l.KP = kp
				return l, nil
			}
		}
	} else if !errors.Is(err1, os.ErrNotExist) && err1 != nil {
		return nil, err1
	}
	if err := l.issue(); err != nil {
		return nil, err
	}
	return l, nil
}

func (l *Leaf) fresh(c *x509.Certificate) bool {
	if time.Until(c.NotAfter) < pki.RenewBefore {
		return false
	}
	if c.CheckSignatureFrom(l.ca.Cert) != nil {
		return false
	}
	if !slices.Equal(c.DNSNames, l.opts.DNSNames) || len(c.IPAddresses) != len(l.opts.IPs) {
		return false
	}
	for i := range c.IPAddresses {
		if !c.IPAddresses[i].Equal(l.opts.IPs[i]) {
			return false
		}
	}
	return true
}

func (l *Leaf) issue() error {
	key, err := pki.GenerateKey()
	if err != nil {
		return err
	}
	_, certPEM, err := l.ca.Issue(&key.PublicKey, l.opts)
	if err != nil {
		return err
	}
	keyPEM, err := pki.EncodeKey(key)
	if err != nil {
		return err
	}
	if err := pki.WriteFileAtomic(filepath.Join(l.dir, l.name+".key"), keyPEM, 0o600); err != nil {
		return err
	}
	if err := pki.WriteFileAtomic(filepath.Join(l.dir, l.name+".crt"), certPEM, 0o644); err != nil {
		return err
	}
	if l.KP == nil {
		l.KP, err = pki.NewKeyPair(certPEM, key)
		return err
	}
	return l.KP.Set(certPEM, key)
}

// RenewLoop re-issues the certificate before it expires.
func (l *Leaf) RenewLoop(ctx context.Context) {
	t := time.NewTicker(6 * time.Hour)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			if time.Until(l.KP.Leaf().NotAfter) < pki.RenewBefore {
				if err := l.issue(); err != nil {
					slog.Error("renew certificate", "name", l.name, "err", err)
				} else {
					slog.Info("certificate renewed", "name", l.name, "not_after", l.KP.Leaf().NotAfter)
				}
			}
		}
	}
}

// HubOptions describes the hub's agent-facing identity.
func HubOptions() pki.LeafOptions {
	return pki.LeafOptions{
		CommonName:  pki.HubName,
		DNSNames:    []string{pki.HubName},
		ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth, x509.ExtKeyUsageClientAuth},
	}
}

// UIOptions describes the UI certificate for the given host names / IPs.
func UIOptions(hosts []string) pki.LeafOptions {
	opts := pki.LeafOptions{CommonName: "NexOS", ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}}
	for _, h := range hosts {
		if ip := net.ParseIP(h); ip != nil {
			opts.IPs = append(opts.IPs, ip)
		} else if h != "" && !slices.Contains(opts.DNSNames, h) {
			opts.DNSNames = append(opts.DNSNames, h)
		}
	}
	if len(opts.DNSNames) > 0 {
		opts.CommonName = opts.DNSNames[0]
	}
	return opts
}
