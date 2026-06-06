package vfs

import (
	"io/fs"
	"testing"
	"testing/fstest"

	"honk/block"
	"honk/kernel/kv"
)

func newKV(t *testing.T, pairs map[string]string) *kv.Store {
	t.Helper()
	s, err := kv.Open(block.NewMemory(256, 512))
	if err != nil {
		t.Fatal(err)
	}
	for k, v := range pairs {
		if err := s.Put(k, []byte(v)); err != nil {
			t.Fatal(err)
		}
	}
	t.Cleanup(func() { s.Close() })
	return s
}

func TestKVFSImplementsFS(t *testing.T) {
	s := newKV(t, map[string]string{
		"motd":          "honk",
		"etc/hostname":  "honk",
		"etc/ip":        "10.0.0.1",
		"notes/todo":    "ship M4",
		"notes/done/m3": "blocks",
	})
	fsys := KVFS(s)
	if err := fstest.TestFS(fsys, "motd", "etc/hostname", "etc/ip", "notes/todo", "notes/done/m3"); err != nil {
		t.Fatal(err)
	}
}

func TestOverlayShadowAndMerge(t *testing.T) {
	lower := fstest.MapFS{
		"motd":     {Data: []byte("core motd")},
		"core.txt": {Data: []byte("from core")},
		"etc/os":   {Data: []byte("honk")},
	}
	upper := KVFS(newKV(t, map[string]string{
		"motd":     "user motd", // shadows the core motd
		"user.txt": "from user",
		"etc/ip":   "10.0.0.1",
	}))
	o := Overlay(upper, lower)

	// upper shadows lower for a file present in both.
	if b, err := fs.ReadFile(o, "motd"); err != nil || string(b) != "user motd" {
		t.Fatalf("motd = %q, %v; want \"user motd\"", b, err)
	}
	// lower-only file is visible.
	if b, err := fs.ReadFile(o, "core.txt"); err != nil || string(b) != "from core" {
		t.Fatalf("core.txt = %q, %v", b, err)
	}
	// upper-only file is visible.
	if b, err := fs.ReadFile(o, "user.txt"); err != nil || string(b) != "from user" {
		t.Fatalf("user.txt = %q, %v", b, err)
	}

	// root listing merges both layers, deduped.
	got := names(t, o, ".")
	want := []string{"core.txt", "etc", "motd", "user.txt"}
	if !equal(got, want) {
		t.Fatalf("ReadDir(.) = %v, want %v", got, want)
	}
	// subdirectory present in both layers merges.
	got = names(t, o, "etc")
	want = []string{"ip", "os"}
	if !equal(got, want) {
		t.Fatalf("ReadDir(etc) = %v, want %v", got, want)
	}
}

func names(t *testing.T, fsys fs.FS, dir string) []string {
	t.Helper()
	es, err := fs.ReadDir(fsys, dir)
	if err != nil {
		t.Fatal(err)
	}
	out := make([]string, len(es))
	for i, e := range es {
		out[i] = e.Name()
	}
	return out
}

func equal(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
