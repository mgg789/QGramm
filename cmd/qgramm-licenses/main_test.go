package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestAssemblePreservesFullLicenseTextsAndOrder(t *testing.T) {
	dir := t.TempDir()
	text := "Copyright upstream\nPermission granted, with conditions.\n"
	if e := os.WriteFile(filepath.Join(dir, "LICENSE"), []byte(text), 0600); e != nil {
		t.Fatal(e)
	}
	if e := os.WriteFile(filepath.Join(dir, "NOTICE.txt"), []byte("Complete notice\n"), 0600); e != nil {
		t.Fatal(e)
	}
	data, e := assemble([]module{{Path: "z.example/module", Version: "v2", Dir: dir}, {Path: "a.example/module", Version: "v1", Dir: dir}})
	if e != nil {
		t.Fatal(e)
	}
	s := string(data)
	if strings.Count(s, text) != 2 || strings.Count(s, "Complete notice\n") != 2 {
		t.Fatal("license text lost")
	}
	if strings.Index(s, "a.example/module") > strings.Index(s, "z.example/module") {
		t.Fatal("inventory not deterministic")
	}
}
func TestMissingLicenseFailsClosed(t *testing.T) {
	if _, e := assemble([]module{{Path: "example/module", Dir: t.TempDir()}}); e == nil {
		t.Fatal("missing license silently accepted")
	}
}
