-- Opaque, random WebAuthn user handle (generated on first passkey).
ALTER TABLE users ADD COLUMN webauthn_handle BLOB;

-- One-time invitations: the invited person sets their own password and
-- second factor; an admin never learns them.
CREATE TABLE user_invites (
    token_hash BLOB    PRIMARY KEY,
    user_id    INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    expires_at INTEGER NOT NULL,
    created_by INTEGER
);
CREATE INDEX user_invites_user ON user_invites(user_id);

CREATE TABLE webauthn_credentials (
    id           BLOB    PRIMARY KEY,
    user_id      INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    name         TEXT    NOT NULL,
    credential   TEXT    NOT NULL, -- JSON of webauthn.Credential (public key, counters)
    created_at   INTEGER NOT NULL,
    last_used_at INTEGER
);
CREATE INDEX webauthn_credentials_user ON webauthn_credentials(user_id);
