package auth

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/pquerna/otp"
	"github.com/pquerna/otp/totp"
)

func TestPasswordHash(t *testing.T) {
	h, err := HashPassword("correct horse battery")
	if err != nil {
		t.Fatal(err)
	}
	if !VerifyPassword(h, "correct horse battery") {
		t.Fatal("valid password rejected")
	}
	if VerifyPassword(h, "correct horse batterY") {
		t.Fatal("wrong password accepted")
	}
	if VerifyPassword("garbage", "x") {
		t.Fatal("malformed hash accepted")
	}
}

func TestCheckTOTP(t *testing.T) {
	key, err := NewTOTP("admin")
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	code, _ := totp.GenerateCodeCustom(key.Secret(), now, totp.ValidateOpts{Period: 30, Digits: otp.DigitsSix, Algorithm: otp.AlgorithmSHA1})
	step, ok := CheckTOTP(key.Secret(), code, now)
	if !ok || step != now.Unix()/30 {
		t.Fatalf("valid code rejected: ok=%v step=%d", ok, step)
	}
	if _, ok := CheckTOTP(key.Secret(), code, now.Add(5*time.Minute)); ok {
		t.Fatal("stale code accepted")
	}
	if _, ok := CheckTOTP(key.Secret(), "12345", now); ok {
		t.Fatal("short code accepted")
	}
}

func TestSealer(t *testing.T) {
	path := filepath.Join(t.TempDir(), "secret.key")
	s, err := LoadOrCreateSealer(path)
	if err != nil {
		t.Fatal(err)
	}
	sealed := s.Seal("JBSWY3DPEHPK3PXP")
	s2, err := LoadOrCreateSealer(path)
	if err != nil {
		t.Fatal(err)
	}
	plain, err := s2.Open(sealed)
	if err != nil || plain != "JBSWY3DPEHPK3PXP" {
		t.Fatalf("open: %q %v", plain, err)
	}
	if _, err := s2.Open(sealed[:len(sealed)-2] + "AA"); err == nil {
		t.Fatal("tampered value accepted")
	}
}

func TestLimiter(t *testing.T) {
	l := NewLimiter(time.Hour, 3)
	for i := 0; i < 3; i++ {
		if !l.Allow("a") {
			t.Fatal("burst denied")
		}
	}
	if l.Allow("a") {
		t.Fatal("limit not enforced")
	}
	if !l.Allow("b") {
		t.Fatal("keys not independent")
	}
}
