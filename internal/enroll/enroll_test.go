package enroll

import "testing"

func TestParseBundleTolerantInput(t *testing.T) {
	want := Bundle{HubAddr: "10.66.0.1:7444", CAFingerprint: "sha256:ab", AgentID: "abcdefgh12", Secret: "s3cret"}
	enc := want.Encode()
	for name, in := range map[string]string{
		"plain":     enc,
		"spaces":    "  " + enc + "\n",
		"env line":  "NEXOS_ENROLL=" + enc,
		"whole env": "NEXOS_LISTEN=127.0.0.1:7443\nNEXOS_ENROLL=" + enc + "\n",
		"quoted":    "'" + enc + "'",
	} {
		got, err := ParseBundle(in)
		if err != nil || got != want {
			t.Errorf("%s: got %+v, %v", name, got, err)
		}
	}
	if _, err := ParseBundle("NEXOS_LISTEN=127.0.0.1:7443"); err == nil {
		t.Error("input without a bundle accepted")
	}
}
