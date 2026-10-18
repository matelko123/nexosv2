package main

import (
	"context"
	"fmt"
	"path/filepath"
	"time"

	"nexos/hub/auth"
	"nexos/hub/store"
)

// resetUser is the break-glass recovery for a locked-out user (e.g. the only
// admin forgot the password): it clears the user's credentials and prints a
// one-time invitation link. Without a name it lists the users. It runs inside
// the hub container (docker exec nexos-hub /nexos-hub reset-user <username>),
// so it needs Docker access on the hub's host, not anything reachable over
// the network.
func resetUser(args []string) error {
	if len(args) > 1 {
		return fmt.Errorf("usage: nexos-hub reset-user [username]")
	}
	st, err := store.Open(filepath.Join(env("NEXOS_DATA_DIR", "/data"), "nexos.db"))
	if err != nil {
		return err
	}
	defer st.Close()
	ctx := context.Background()
	if len(args) == 0 {
		users, err := st.ListUsers(ctx)
		if err != nil {
			return err
		}
		fmt.Println("usage: nexos-hub reset-user <username>\n\nusers:")
		for _, u := range users {
			fmt.Printf("  %-32s %s\n", u.Username, u.Role)
		}
		return nil
	}
	u, err := st.UserByUsername(ctx, args[0])
	if err != nil {
		return fmt.Errorf("user %q: %w", args[0], err)
	}
	if err := st.RecoverUserCredentials(ctx, u.ID); err != nil {
		return err
	}
	token := auth.RandomToken(32)
	exp := time.Now().Add(48 * time.Hour)
	if err := st.CreateInvite(ctx, u.ID, u.ID, token, exp); err != nil {
		return err
	}
	_ = st.Audit(ctx, store.AuditEntry{Username: "cli", Action: "user.reset", Target: fmt.Sprint(u.ID), Success: true, Detail: "reset-user command"})
	fmt.Printf("Credentials of %q cleared; all sessions and passkeys removed.\n", u.Username)
	fmt.Printf("Open within 48 h to set a new password and TOTP:\n\n  https://<hub-address>/invite/%s\n\n", token)
	return nil
}
