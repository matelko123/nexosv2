package main

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"nexos/hub/store"
)

func TestResetUser(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("NEXOS_DATA_DIR", dir)
	ctx := context.Background()
	st, err := store.Open(filepath.Join(dir, "nexos.db"))
	if err != nil {
		t.Fatal(err)
	}
	if err := st.CreateFirstUser(ctx, &store.User{Username: "admin", PasswordHash: "x", TOTPSecret: "y", Role: "admin"}); err != nil {
		t.Fatal(err)
	}
	u, _ := st.UserByUsername(ctx, "admin")
	if err := st.CreateSession(ctx, "tok", store.Session{UserID: u.ID, CreatedAt: time.Now(), ExpiresAt: time.Now().Add(time.Hour), LastSeenAt: time.Now()}, "", ""); err != nil {
		t.Fatal(err)
	}
	st.Close()

	if err := resetUser([]string{"admin"}); err != nil {
		t.Fatal(err)
	}
	st, _ = store.Open(filepath.Join(dir, "nexos.db"))
	defer st.Close()
	u, _ = st.UserByUsername(ctx, "admin")
	if u.PasswordHash != "" || u.TOTPSecret != "" {
		t.Fatal("credentials not cleared")
	}
	if s, _ := st.SessionByToken(ctx, "tok"); s != nil {
		t.Fatal("session survived reset")
	}
	if err := resetUser([]string{"ghost"}); err == nil {
		t.Fatal("unknown user accepted")
	}
}
