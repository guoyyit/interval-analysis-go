package loader

import (
	"testing"
)

func TestLoadModulePackage(t *testing.T) {
	// Loads a real package inside this module (the demo testdata).
	l := New(false)
	pkgs, err := l.LoadDir("../internal/testdata/demo")
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if len(pkgs) == 0 {
		t.Fatal("expected at least one package")
	}
	p := pkgs[0]
	if p.Types == nil || p.Info == nil || len(p.Files) == 0 {
		t.Fatal("package not fully type-checked")
	}
	if p.Hash == "" {
		t.Fatal("expected non-empty source hash")
	}
	// Same content must hash identically (basis for incremental reuse).
	pkgs2, _ := l.LoadDir("../internal/testdata/demo")
	if pkgs2[0].Hash != p.Hash {
		t.Fatal("identical source must yield identical hash")
	}
}
