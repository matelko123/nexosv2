package docker

import "testing"

func TestValidateUpdateImage(t *testing.T) {
	t.Setenv("NEXOS_UPDATE_IMAGE_PREFIX", "")
	ok := []struct{ img, role string }{
		{"ghcr.io/matelko123/nexosv2-agent:v1.2.0", "agent"},
		{"ghcr.io/matelko123/nexosv2-hub:v1.2.0", "hub"},
	}
	for _, c := range ok {
		if _, err := validateUpdateImage(c.img, c.role); err != nil {
			t.Errorf("%s rejected: %v", c.img, err)
		}
	}
	bad := []struct{ img, role string }{
		{"ghcr.io/matelko123/nexosv2-hub:v1.2.0", "agent"},          // wrong component
		{"ghcr.io/evil/nexosv2-agent:v1.2.0", "agent"},              // other owner
		{"ghcr.io/matelko123/nexosv2-agent", "agent"},               // no tag
		{"ghcr.io/matelko123/nexosv2-agent-x:v1", "agent"},          // look-alike name
		{"docker.io/library/alpine:3", "agent"},                     // unrelated
		{"ghcr.io/matelko123/nexosv2-agent@sha256:" + "a", "agent"}, // invalid
	}
	for _, c := range bad {
		if _, err := validateUpdateImage(c.img, c.role); err == nil {
			t.Errorf("%s accepted for %s", c.img, c.role)
		}
	}
}
