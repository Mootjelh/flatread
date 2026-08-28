package flatread

import (
	"encoding/binary"
	"fmt"
	"sort"
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
// lives in. Nothing forbids this. An encoder would not emit it, but a corrupt
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

func TestDumpPointsOutUnions(t *testing.T) {
	out := Dump(unionSample())

	for _, want := range []string{
		"maybe a union tag, value in slot 6",
		"maybe union tags for the 3 tables in slot 10",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("Dump() missing %q\n---\n%s", want, out)
		}
	}
}

// The ordinary sample has no unions, so a hint on it would be a false positive
// sending a reader after a pairing that is not there.
func TestDumpDoesNotInventUnions(t *testing.T) {
	if out := Dump(sample()); strings.Contains(out, "union") {
		t.Errorf("Dump() claims a union where there is none\n---\n%s", out)
	}
}

// --- vectors of structs ------------------------------------------------------

// Without a schema the dump cannot know, and says the wrong thing plausibly:
// structVectorSample holds 24 bytes of records and reads as a 3-byte vector,
// because the count of a struct vector is its element count. This is the state
// StructSize exists to fix, so pin it, otherwise the test below could pass for
// the wrong reason.
func TestDumpWithoutStructSizeReadsAStructVectorAsBytes(t *testing.T) {
	out := Dump(structVectorSample())
	if !strings.Contains(out, "slot 4   bytes    3") {
		t.Errorf("Dump() = %q, want the byte-vector reading it has always given", out)
	}
}

func TestDumpStructVector(t *testing.T) {
	out := DumpWith(structVectorSample(), DumpOptions{
		StructSize: func(path []uint16) int {
			if len(path) == 1 && path[0] == 4 {
				return 8
			}
			return 0
		},
	})

	for _, want := range []string{
		"slot 4   structs  3 x 8 bytes",
		"[0] 01 00 00 00 02 00 00 00   u32 1 2",
		"[1] 03 00 00 00 04 00 00 00   u32 3 4",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("Dump() missing %q\n---\n%s", want, out)
		}
	}
	if strings.Contains(out, "slot 4   bytes") {
		t.Errorf("the byte-vector reading is still there\n---\n%s", out)
	}
}

// The default breadth is 3 and the sample holds exactly 3, so ask for fewer to
// reach the remainder line at all.
func TestDumpStructVectorRespectsMaxVectorElems(t *testing.T) {
	out := DumpWith(structVectorSample(), DumpOptions{
		MaxVectorElems: 2,
		StructSize:     func([]uint16) int { return 8 },
	})

	if !strings.Contains(out, "... 1 more") {
		t.Errorf("Dump() did not summarise the remainder\n---\n%s", out)
	}
	if strings.Contains(out, "[2]") {
		t.Errorf("Dump() expanded past MaxVectorElems\n---\n%s", out)
	}
}

// A size that cannot fit is reported rather than quietly falling back to the
// byte-vector rendering. The caller stated a schema and is owed an answer to
// that question, not to a different one.
func TestDumpStructSizeThatDoesNotFit(t *testing.T) {
	out := DumpWith(structVectorSample(), DumpOptions{
		StructSize: func([]uint16) int { return 4096 },
	})

	if !strings.Contains(out, "no vector of 4096-byte elements fits here") {
		t.Errorf("Dump() = %q, want it to say the size does not fit", out)
	}
	if strings.Contains(out, "slot 4   bytes") {
		t.Errorf("Dump() fell back to the byte reading instead of reporting\n---\n%s", out)
	}
}

func TestDumpStructSizeIsAskedWithThePathFromTheRoot(t *testing.T) {
	seen := map[string]bool{}
	_ = DumpWith(sample(), DumpOptions{
		StructSize: func(path []uint16) int {
			seen[fmt.Sprint(path)] = true
			return 0
		},
	})

	// [10 4] is slot 4 of the table in root slot 10, and [12 4] is slot 4 of
	// the tables in the vector at root slot 12. The element index is absent
	// from both, which is the point: a path names a place in the schema.
	for _, want := range []string{"[6]", "[10]", "[10 4]", "[12 4]"} {
		if !seen[want] {
			t.Errorf("StructSize was never asked about %s, saw %v", want, keys(seen))
		}
	}
}

// There is deliberately no test that a kept path survives the rest of the walk.
// One was written and it could not fail: the walk hands out a fresh slice per
// slot, and even the append version it replaced happened to reallocate every
// time at the depth any sample here reaches. Catching that regression needs a
// buffer nested about four deep, which does not exist yet. The comment in
// dumpTable carries the reasoning instead.

func keys(m any) []string {
	var out []string
	switch m := m.(type) {
	case map[string]bool:
		for k := range m {
			out = append(out, k)
		}
	case map[string]int:
		for k := range m {
			out = append(out, k)
		}
	}
	sort.Strings(out)
	return out
}

// --- focusing on one branch --------------------------------------------------

func TestDumpOnly(t *testing.T) {
	full := Dump(sample())
	// The control. If these were not in the full dump, the assertions below
	// would pass on a dump that lost them for some other reason.
	for _, want := range []string{`slot 6   string   "hello"`, "slot 10  table"} {
		if !strings.Contains(full, want) {
			t.Fatalf("the full dump is missing %q, so this test cannot mean anything\n---\n%s", want, full)
		}
	}

	out := DumpWith(sample(), DumpOptions{Only: []uint16{10}})

	if !strings.Contains(out, "slot 10  table") {
		t.Errorf("Dump(Only 10) lost the slot that was asked for\n---\n%s", out)
	}
	if !strings.Contains(out, "u32=7") {
		t.Errorf("Dump(Only 10) did not descend into it\n---\n%s", out)
	}
	for _, gone := range []string{`"hello"`, "slot 12", "u32=4242"} {
		if strings.Contains(out, gone) {
			t.Errorf("Dump(Only 10) still shows %q\n---\n%s", gone, out)
		}
	}
}

// The slots on the way to the target are kept, so the output says where in the
// buffer you are looking.
func TestDumpOnlyKeepsTheSlotsOnTheWay(t *testing.T) {
	out := DumpWith(sample(), DumpOptions{Only: []uint16{12, 4}})

	if !strings.Contains(out, "slot 12  vector   2 tables") {
		t.Errorf("Dump(Only 12.4) dropped the vector it descended through\n---\n%s", out)
	}
	if !strings.Contains(out, "u32=11") || !strings.Contains(out, "u32=22") {
		t.Errorf("Dump(Only 12.4) did not reach slot 4 of both elements\n---\n%s", out)
	}
	if strings.Contains(out, `"hello"`) {
		t.Errorf("Dump(Only 12.4) still shows an unrelated branch\n---\n%s", out)
	}
}

// A path that is not there says so. An empty dump reads exactly like a buffer
// with nothing in it, and telling those apart is most of what this is for.
func TestDumpOnlyReportsAPathThatIsNotThere(t *testing.T) {
	out := DumpWith(sample(), DumpOptions{Only: []uint16{99}})
	if !strings.Contains(out, "no slot 99 in this buffer") {
		t.Errorf("Dump(Only 99) = %q, want it to say the path is not there", out)
	}
}

// The harder half: slot 10 exists and 10.99 does not, so a breadcrumb line is
// printed and the dump is not empty. Deciding on output length would call this
// a hit.
func TestDumpOnlyReportsAMissingLeafUnderAnExistingParent(t *testing.T) {
	out := DumpWith(sample(), DumpOptions{Only: []uint16{10, 99}})

	if !strings.Contains(out, "slot 10  table") {
		t.Errorf("expected the parent to still be shown\n---\n%s", out)
	}
	if !strings.Contains(out, "no slot 10.99 in this buffer") {
		t.Errorf("Dump(Only 10.99) = %q, want it to say the leaf is not there", out)
	}
}

func TestDumpOnlyEmptyMeansEverything(t *testing.T) {
	full := Dump(sample())
	out := DumpWith(sample(), DumpOptions{Only: nil})
	if out != full {
		t.Errorf("an empty Only changed the dump\n got %s\nwant %s", out, full)
	}
}

func TestOnPath(t *testing.T) {
	cases := []struct {
		only, path []uint16
		want       bool
	}{
		{nil, []uint16{4}, true},              // no filter takes everything
		{[]uint16{10}, []uint16{10}, true},    // the target itself
		{[]uint16{10}, []uint16{10, 4}, true}, // inside it
		{[]uint16{10}, []uint16{4}, false},    // a sibling
		{[]uint16{10, 4}, []uint16{10}, true}, // on the way to it
		{[]uint16{10, 4}, []uint16{10, 6}, false},
		{[]uint16{10, 4}, []uint16{10, 4, 8}, true},
		{[]uint16{10}, nil, true}, // the root is on the way to everything
	}

	for _, c := range cases {
		if got := OnPath(c.only, c.path); got != c.want {
			t.Errorf("OnPath(%v, %v) = %v, want %v", c.only, c.path, got, c.want)
		}
	}
}
