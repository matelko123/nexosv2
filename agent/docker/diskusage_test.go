package docker

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestSkippedPath(t *testing.T) {
	for p, want := range map[string]bool{
		"/": true, "/proc": true, "/var/run/docker.sock": true, "/var/lib/docker/volumes/x": true,
		"/srv/nextcloud": false, "/running": false, "/devices": false,
	} {
		if got := skippedPath(p); got != want {
			t.Errorf("skippedPath(%q) = %v, want %v", p, got, want)
		}
	}
}

func TestMeasure(t *testing.T) {
	host := t.TempDir()
	dir := filepath.Join(host, "srv", "app")
	if err := os.MkdirAll(filepath.Join(dir, "sub"), 0o755); err != nil {
		t.Fatal(err)
	}
	for name, size := range map[string]int{"a": 5000, "sub/b": 70000} {
		if err := os.WriteFile(filepath.Join(dir, name), make([]byte, size), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	d := NewDiskUsage(nil, host)
	u := d.measure(context.Background(), "/srv/app", time.Now().Add(time.Minute))
	if u.Error != "" || u.Partial || u.SizeBytes < 75000 {
		t.Fatalf("measure: %+v", u)
	}
	if u := d.measure(context.Background(), "/srv/missing", time.Now().Add(time.Minute)); u.Error == "" {
		t.Fatal("missing path should report an error")
	}
	if u := d.measure(context.Background(), "/srv/app", time.Now().Add(-time.Second)); u.Error == "" || u.SizeBytes != 0 {
		t.Fatalf("exhausted budget: %+v", u)
	}
}
