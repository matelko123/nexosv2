package pki

import (
	"crypto/tls"
	"crypto/x509"
	"errors"
	"net"
	"testing"
	"time"
)

type fixture struct {
	ca      *CA
	hub     *KeyPair
	agent   *KeyPair
	agentID string
}

func newFixture(t *testing.T) fixture {
	t.Helper()
	ca, err := NewCA("test")
	if err != nil {
		t.Fatal(err)
	}
	hub := issueHub(t, ca)
	id := NewAgentID()
	key, _ := GenerateKey()
	csr, err := CreateCSR(key, "ignored")
	if err != nil {
		t.Fatal(err)
	}
	_, certPEM, err := ca.SignAgentCSR(csr, id)
	if err != nil {
		t.Fatal(err)
	}
	agent, err := NewKeyPair(certPEM, key)
	if err != nil {
		t.Fatal(err)
	}
	return fixture{ca: ca, hub: hub, agent: agent, agentID: id}
}

func issueHub(t *testing.T, ca *CA) *KeyPair {
	t.Helper()
	key, _ := GenerateKey()
	_, pemBytes, err := ca.Issue(&key.PublicKey, LeafOptions{
		CommonName:  HubName,
		DNSNames:    []string{HubName},
		ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth, x509.ExtKeyUsageClientAuth},
	})
	if err != nil {
		t.Fatal(err)
	}
	kp, err := NewKeyPair(pemBytes, key)
	if err != nil {
		t.Fatal(err)
	}
	return kp
}

// handshake runs a TLS handshake between the two configs and returns the
// first error seen on either side.
func handshake(t *testing.T, server, client *tls.Config) error {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	srvErr := make(chan error, 1)
	go func() {
		c, err := ln.Accept()
		if err != nil {
			srvErr <- err
			return
		}
		defer c.Close()
		c.SetDeadline(time.Now().Add(5 * time.Second))
		s := tls.Server(c, server)
		err = s.Handshake()
		if err == nil {
			// TLS 1.3 client cert verification completes on first read.
			_, err = s.Read(make([]byte, 1))
		}
		srvErr <- err
	}()
	conn, err := net.Dial("tcp", ln.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	conn.SetDeadline(time.Now().Add(5 * time.Second))
	cl := tls.Client(conn, client)
	cliErr := cl.Handshake()
	if cliErr == nil {
		_, cliErr = cl.Write([]byte{1})
	}
	sErr := <-srvErr
	if cliErr != nil {
		return cliErr
	}
	return sErr
}

func TestHubAgentHandshake(t *testing.T) {
	f := newFixture(t)
	err := handshake(t, AgentServerConfig(f.agent, f.ca.Pool()), HubClientConfig(f.hub, f.ca.Pool(), f.agentID, nil))
	if err != nil {
		t.Fatalf("expected success, got %v", err)
	}
}

func TestRejectsClientWithoutCertificate(t *testing.T) {
	f := newFixture(t)
	client := &tls.Config{MinVersion: tls.VersionTLS13, RootCAs: f.ca.Pool(), ServerName: AgentName(f.agentID)}
	if err := handshake(t, AgentServerConfig(f.agent, f.ca.Pool()), client); err == nil {
		t.Fatal("expected rejection")
	}
}

func TestRejectsForeignCA(t *testing.T) {
	f := newFixture(t)
	other, _ := NewCA("evil")
	evilHub := issueHub(t, other)
	if err := handshake(t, AgentServerConfig(f.agent, f.ca.Pool()), HubClientConfig(evilHub, f.ca.Pool(), f.agentID, nil)); err == nil {
		t.Fatal("expected rejection of hub cert from foreign CA")
	}
}

func TestRejectsAgentCertAsClient(t *testing.T) {
	f := newFixture(t)
	// Another agent (same CA) must not be able to impersonate the hub.
	if err := handshake(t, AgentServerConfig(f.agent, f.ca.Pool()), HubClientConfig(f.agent, f.ca.Pool(), f.agentID, nil)); err == nil {
		t.Fatal("expected rejection of non-hub client certificate")
	}
}

func TestRejectsWrongAgentIdentity(t *testing.T) {
	f := newFixture(t)
	if err := handshake(t, AgentServerConfig(f.agent, f.ca.Pool()), HubClientConfig(f.hub, f.ca.Pool(), NewAgentID(), nil)); err == nil {
		t.Fatal("expected rejection of unexpected agent identity")
	}
}

func TestPinRejection(t *testing.T) {
	f := newFixture(t)
	pinErr := errors.New("not pinned")
	err := handshake(t, AgentServerConfig(f.agent, f.ca.Pool()), HubClientConfig(f.hub, f.ca.Pool(), f.agentID, func(string) error { return pinErr }))
	if err == nil {
		t.Fatal("expected pin rejection")
	}
	var got string
	err = handshake(t, AgentServerConfig(f.agent, f.ca.Pool()), HubClientConfig(f.hub, f.ca.Pool(), f.agentID, func(fp string) error { got = fp; return nil }))
	if err != nil || got != Fingerprint(f.agent.Leaf()) {
		t.Fatalf("pin callback: err=%v fp=%q", err, got)
	}
}

func TestRejectsExpiredCertificate(t *testing.T) {
	f := newFixture(t)
	key, _ := GenerateKey()
	_, pemBytes, err := f.ca.Issue(&key.PublicKey, LeafOptions{
		CommonName: HubName, DNSNames: []string{HubName},
		ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth}, Validity: time.Nanosecond,
	})
	if err != nil {
		t.Fatal(err)
	}
	time.Sleep(5 * time.Millisecond)
	expired, _ := NewKeyPair(pemBytes, key)
	if err := handshake(t, AgentServerConfig(f.agent, f.ca.Pool()), HubClientConfig(expired, f.ca.Pool(), f.agentID, nil)); err == nil {
		t.Fatal("expected rejection of expired certificate")
	}
}

func TestPinnedCAEnrollment(t *testing.T) {
	f := newFixture(t)
	server := ServerConfig(f.hub)
	server.GetCertificate = f.hub.WithChain(f.ca.Cert)

	var gotCA *x509.Certificate
	if err := handshake(t, server, PinnedCAClientConfig(Fingerprint(f.ca.Cert), func(c *x509.Certificate) { gotCA = c })); err != nil {
		t.Fatalf("expected success, got %v", err)
	}
	if gotCA == nil || !gotCA.Equal(f.ca.Cert) {
		t.Fatal("CA not delivered")
	}

	other, _ := NewCA("other")
	if err := handshake(t, server, PinnedCAClientConfig(Fingerprint(other.Cert), nil)); err == nil {
		t.Fatal("expected fingerprint mismatch")
	}

	// A server holding a cert from a different CA but forwarding the real CA
	// certificate in its chain must still fail chain verification.
	evil := issueHub(t, other)
	evilServer := ServerConfig(evil)
	evilServer.GetCertificate = evil.WithChain(f.ca.Cert)
	if err := handshake(t, evilServer, PinnedCAClientConfig(Fingerprint(f.ca.Cert), nil)); err == nil {
		t.Fatal("expected chain verification failure")
	}
}

func TestSignAgentCSRIgnoresRequestedNames(t *testing.T) {
	ca, _ := NewCA("test")
	key, _ := GenerateKey()
	csr, _ := CreateCSR(key, HubName)
	cert, _, err := ca.SignAgentCSR(csr, "abcdefgh12")
	if err != nil {
		t.Fatal(err)
	}
	if cert.VerifyHostname(HubName) == nil {
		t.Fatal("agent certificate must not be valid for the hub name")
	}
	if _, _, err := ca.SignAgentCSR(csr, "../bad"); err == nil {
		t.Fatal("expected invalid id error")
	}
}

func TestLoadOrCreateCA(t *testing.T) {
	dir := t.TempDir()
	a, err := LoadOrCreateCA(dir)
	if err != nil {
		t.Fatal(err)
	}
	b, err := LoadOrCreateCA(dir)
	if err != nil {
		t.Fatal(err)
	}
	if !a.Cert.Equal(b.Cert) {
		t.Fatal("CA changed between loads")
	}
}
