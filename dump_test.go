package flatread

import (
	"encoding/binary"
	"fmt"
	"strings"
	"testing"
)

func TestDump(t *testing.T) {
	out := Dump(sample())

	for _, want := range []string{
		"root @0x40",
		`slot 6   string   "hello"`,
		"slot 10  table",
		"slot 12  vector   2 tables",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("Dump() missing %q\n---\n%s", want, out)
		}
	}
}

func TestDumpAnnotateHook(t *testing.T) {
	out := DumpWith(sample(), DumpOptions{
		Annotate: func(b []byte) string { return fmt.Sprintf("len=%d", len(b)) },
	})
	if !strings.Contains(out, "len=") {
		t.Errorf("Annotate was never called\n---\n%s", out)
	}
}

func TestDumpMalformedBufferDescribesTheError(t *testing.T) {
	out := Dump([]byte{1, 2})
	if !strings.Contains(out, "too small") {
		t.Errorf("Dump(short buffer) = %q, want an error description", out)
	}
}

// selfReferential builds a buffer whose only field points back at the table it
// lives in. Nothing forbids this — an encoder would not emit it, but a corrupt
// file or a hostile one can, and a naive walker recurses forever.
func selfReferential() []byte {
	const (
		vt   = 8
		root = 16
	)
	b := make([]byte, 32)
	binary.LittleEndian.PutUint32(b[0:], root)
	binary.LittleEndian.PutUint16(b[vt+0:], 6)
	binary.LittleEndian.PutUint16(b[vt+2:], 8)
	binary.LittleEndian.PutUint16(b[vt+4:], 4)
	binary.LittleEndian.PutUint32(b[root:], uint32(root-vt))
	// A uoffset of -4, which wraps to point back at the table itself.
	binary.LittleEndian.PutUint32(b[root+4:], ^uint32(3))
	return b
}

func TestCycleTerminates(t *testing.T) {
	buf := selfReferential()

	r, err := Root(buf)
	if err != nil {
		t.Fatalf("Root: %v", err)
	}
	if sub, ok := r.Table(4); ok && sub.Pos() != r.Pos() {
		t.Fatalf("fixture is not cyclic: sub.Pos() = %d, root = %d", sub.Pos(), r.Pos())
	}

	// The assertion is that this returns at all. A walker without a visited set
	// hangs here until the test times out.
	if out := Dump(buf); out == "" {
		t.Error("Dump returned nothing")
	}
}
