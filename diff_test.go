package flatread

import (
	"encoding/binary"
	"testing"
)

// mutated copies the sample and applies one edit, so each test changes exactly
// the thing it is about and the layout stays valid.
func mutated(edit func(b []byte)) []byte {
	b := append([]byte(nil), sample()...)
	edit(b)
	return b
}

func diffOf(t *testing.T, a, b []byte) []Change {
	t.Helper()
	ra, err := Root(a)
	if err != nil {
		t.Fatalf("Root(a): %v", err)
	}
	rb, err := Root(b)
	if err != nil {
		t.Fatalf("Root(b): %v", err)
	}
	return Diff(ra, rb)
}

func TestDiffFindsNothingWhenIdentical(t *testing.T) {
	if got := diffOf(t, sample(), sample()); got != nil {
		t.Errorf("Diff of two identical buffers = %v, want nil", got)
	}
}

func TestDiffReportsSlotPaths(t *testing.T) {
	tests := []struct {
		name string
		edit func(b []byte)
		path string
		a, b string
	}{
		{
			name: "scalar in the root",
			edit: func(b []byte) { binary.LittleEndian.PutUint32(b[rootPos+4:], 9999) },
			path: "4", a: "4242", b: "9999",
		},
		{
			name: "string contents",
			edit: func(b []byte) { copy(b[strPos+4:], "world") },
			path: "6", a: `"hello"`, b: `"world"`,
		},
		{
			name: "a field of a nested table",
			edit: func(b []byte) { binary.LittleEndian.PutUint32(b[nestPos+4:], 8) },
			path: "10.4", a: "7", b: "8",
		},
		{
			name: "an element of a table vector",
			edit: func(b []byte) { binary.LittleEndian.PutUint32(b[elem0Pos+4:], 12) },
			path: "12[0].4", a: "11", b: "12",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := diffOf(t, sample(), mutated(tt.edit))
			if len(got) != 1 {
				t.Fatalf("Diff = %v, want exactly one change", got)
			}
			if got[0].Path != tt.path || got[0].A != tt.a || got[0].B != tt.b {
				t.Errorf("Diff = %+v, want path %q %q -> %q", got[0], tt.path, tt.a, tt.b)
			}
		})
	}
}

// A slot present on one side only is the case a naive walk misses, because it
// iterates one buffer's slots and never sees what the other has.
func TestDiffReportsSlotsPresentOnOneSideOnly(t *testing.T) {
	// Zeroing the vtable entry makes slot 4 absent without moving anything.
	dropped := mutated(func(b []byte) { binary.LittleEndian.PutUint16(b[rootVT+4:], 0) })

	got := diffOf(t, sample(), dropped)
	if len(got) != 1 {
		t.Fatalf("Diff = %v, want exactly one change", got)
	}
	if got[0].Path != "4" || got[0].A != "4242" || got[0].B != "" {
		t.Errorf("Diff = %+v, want slot 4 going absent", got[0])
	}

	// And the other way round, so the union of slots is really a union.
	back := diffOf(t, dropped, sample())
	if len(back) != 1 || back[0].A != "" || back[0].B != "4242" {
		t.Errorf("reversed Diff = %v, want slot 4 appearing", back)
	}
}

func TestChangeStringShowsAbsent(t *testing.T) {
	if got := (Change{Path: "4", A: "1"}).String(); got != "4              1 -> (absent)" {
		t.Errorf("String() = %q", got)
	}
	if got := (Change{Path: "4", B: "1"}).String(); got != "4              (absent) -> 1" {
		t.Errorf("String() = %q", got)
	}
}

// Diff must survive a buffer whose field points back at its own table, the same
// way the dumper does.
func TestDiffTerminatesOnACycle(t *testing.T) {
	a, err := Root(selfReferential())
	if err != nil {
		t.Fatalf("Root: %v", err)
	}
	b, err := Root(selfReferential())
	if err != nil {
		t.Fatalf("Root: %v", err)
	}
	// The assertion is that this returns at all.
	_ = Diff(a, b)
}
