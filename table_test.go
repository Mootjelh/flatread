package flatread

import (
	"encoding/binary"
	"testing"
)

// The fixture is built by hand rather than with a FlatBuffers compiler, on
// purpose: a generated buffer would only prove this package agrees with the
// generator, and the whole premise here is reading buffers nobody generated for
// us. Every offset below is written out so the layout can be checked by eye
// against the diagram in the package doc.
//
//	  0  u32   root offset -> 64
//	  8  root vtable: size 14, so slots 4, 6, 8, 10, 12
//	 64  root table
//	 88  string "hello"
//	100  vector of u32 {1, 2, 3}
//	116  vtable shared by the two element tables
//	124  vector of 2 tables -> 136, 144
//	152  vtable for the nested table
//	160  nested table {slot 4: 7}
const (
	rootVT   = 8
	rootPos  = 64
	strPos   = 88
	vecPos   = 100
	elemVT   = 116
	tvecPos  = 124
	elem0Pos = 136
	elem1Pos = 144
	nestVT   = 152
	nestPos  = 160
	bufLen   = 168
)

func sample() []byte {
	b := make([]byte, bufLen)
	u32 := func(at int, v uint32) { binary.LittleEndian.PutUint32(b[at:], v) }
	u16 := func(at int, v uint16) { binary.LittleEndian.PutUint16(b[at:], v) }

	u32(0, rootPos)

	// Root vtable: 4-byte header then one u16 per slot.
	u16(rootVT+0, 14)  // vtable size in bytes -> slots 4..12
	u16(rootVT+2, 24)  // inline table size
	u16(rootVT+4, 4)   // slot 4  -> table+4
	u16(rootVT+6, 8)   // slot 6  -> table+8
	u16(rootVT+8, 12)  // slot 8  -> table+12
	u16(rootVT+10, 16) // slot 10 -> table+16
	u16(rootVT+12, 20) // slot 12 -> table+20

	// Root table. The soffset is signed and points BACKWARDS to the vtable.
	u32(rootPos+0, uint32(rootPos-rootVT))
	u32(rootPos+4, 4242)                  // slot 4:  inline scalar
	u32(rootPos+8, strPos-(rootPos+8))    // slot 6:  -> string
	u32(rootPos+12, vecPos-(rootPos+12))  // slot 8:  -> vector of u32
	u32(rootPos+16, nestPos-(rootPos+16)) // slot 10: -> nested table
	u32(rootPos+20, tvecPos-(rootPos+20)) // slot 12: -> vector of tables

	// String: length prefix, bytes, NUL terminator.
	u32(strPos, 5)
	copy(b[strPos+4:], "hello")

	// Vector of u32.
	u32(vecPos, 3)
	u32(vecPos+4, 1)
	u32(vecPos+8, 2)
	u32(vecPos+12, 3)

	// One vtable, shared by both element tables, which is what a real encoder
	// does when two tables have the same shape.
	u16(elemVT+0, 6)
	u16(elemVT+2, 8)
	u16(elemVT+4, 4)

	// Vector of offsets to those two tables.
	u32(tvecPos, 2)
	u32(tvecPos+4, elem0Pos-(tvecPos+4))
	u32(tvecPos+8, elem1Pos-(tvecPos+8))

	u32(elem0Pos, uint32(elem0Pos-elemVT))
	u32(elem0Pos+4, 11)
	u32(elem1Pos, uint32(elem1Pos-elemVT))
	u32(elem1Pos+4, 22)

	// Nested table and its own vtable.
	u16(nestVT+0, 6)
	u16(nestVT+2, 8)
	u16(nestVT+4, 4)
	u32(nestPos, uint32(nestPos-nestVT))
	u32(nestPos+4, 7)

	return b
}

func rootOf(t *testing.T) Table {
	t.Helper()
	r, err := Root(sample())
	if err != nil {
		t.Fatalf("Root: %v", err)
	}
	return r
}

func TestSlots(t *testing.T) {
	got := rootOf(t).Slots()
	want := []uint16{4, 6, 8, 10, 12}
	if len(got) != len(want) {
		t.Fatalf("Slots() = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("Slots() = %v, want %v", got, want)
		}
	}
}

func TestScalarAndString(t *testing.T) {
	r := rootOf(t)

	if got := r.Uint32(4); got != 4242 {
		t.Errorf("Uint32(4) = %d, want 4242", got)
	}
	if got := r.String(6); got != "hello" {
		t.Errorf("String(6) = %q, want %q", got, "hello")
	}
	if got := string(r.Bytes(6)); got != "hello" {
		t.Errorf("Bytes(6) = %q, want %q", got, "hello")
	}
}

func TestVectors(t *testing.T) {
	r := rootOf(t)

	if got := r.VectorLen(8); got != 3 {
		t.Fatalf("VectorLen(8) = %d, want 3", got)
	}
	want := []int32{1, 2, 3}
	got := r.Int32Vector(8)
	if len(got) != len(want) {
		t.Fatalf("Int32Vector(8) = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("Int32Vector(8) = %v, want %v", got, want)
		}
	}
	if u := r.Uint32Vector(8); len(u) != 3 || u[2] != 3 {
		t.Errorf("Uint32Vector(8) = %v, want [1 2 3]", u)
	}
}

func TestNestedTable(t *testing.T) {
	r := rootOf(t)

	nested, ok := r.Table(10)
	if !ok {
		t.Fatal("Table(10) reported absent")
	}
	if got := nested.Uint32(4); got != 7 {
		t.Errorf("nested.Uint32(4) = %d, want 7", got)
	}
	if nested.Pos() != nestPos {
		t.Errorf("nested.Pos() = %d, want %d", nested.Pos(), nestPos)
	}
}

func TestVectorOfTables(t *testing.T) {
	r := rootOf(t)

	if got := r.VectorLen(12); got != 2 {
		t.Fatalf("VectorLen(12) = %d, want 2", got)
	}
	for i, want := range []uint32{11, 22} {
		sub, ok := r.TableAt(12, i)
		if !ok {
			t.Fatalf("TableAt(12, %d) reported absent", i)
		}
		if got := sub.Uint32(4); got != want {
			t.Errorf("TableAt(12, %d).Uint32(4) = %d, want %d", i, got, want)
		}
	}
	if _, ok := r.TableAt(12, 2); ok {
		t.Error("TableAt(12, 2) should be out of range")
	}
	if _, ok := r.TableAt(12, -1); ok {
		t.Error("TableAt(12, -1) should be out of range")
	}
}

func TestHasDistinguishesAbsentFromZero(t *testing.T) {
	r := rootOf(t)

	if !r.Has(4) {
		t.Error("Has(4) = false, want true")
	}
	// Slot 14 is past the vtable, so it is absent rather than zero.
	if r.Has(14) {
		t.Error("Has(14) = true, want false")
	}
	if got := r.Uint32(14); got != 0 {
		t.Errorf("Uint32(14) = %d, want 0 for an absent field", got)
	}
}

func TestInvalidSlotsAreRejected(t *testing.T) {
	r := rootOf(t)

	// Slots 0 and 2 are the vtable's own size header, and an odd slot straddles
	// two entries. All three would otherwise return convincing nonsense.
	for _, slot := range []uint16{0, 2, 5, 7} {
		if r.Has(slot) {
			t.Errorf("Has(%d) = true, want false for an invalid slot", slot)
		}
		if got := r.Uint32(slot); got != 0 {
			t.Errorf("Uint32(%d) = %d, want 0 for an invalid slot", slot, got)
		}
	}
}

func TestRootRejectsUnusableBuffers(t *testing.T) {
	tests := []struct {
		name string
		buf  []byte
	}{
		{"empty", nil},
		{"too small", []byte{1, 2, 3, 4}},
		{"root offset past the end", func() []byte {
			b := make([]byte, 16)
			binary.LittleEndian.PutUint32(b, 9999)
			return b
		}()},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := Root(tt.buf); err == nil {
				t.Error("expected an error")
			}
		})
	}
}

func TestGuess(t *testing.T) {
	r := rootOf(t)

	tests := []struct {
		slot uint16
		want Kind
	}{
		{4, KindScalar},
		{6, KindString},
		{10, KindTable},
		{12, KindVectorOfTables},
	}
	for _, tt := range tests {
		if got := r.Guess(tt.slot); got != tt.want {
			t.Errorf("Guess(%d) = %v, want %v", tt.slot, got, tt.want)
		}
	}
}

// TestGuessCannotSeeElementWidth pins a limitation rather than a feature.
//
// Slot 8 is a vector of u32, and Guess calls it bytes. Nothing in a FlatBuffers
// buffer records the element width of a vector, so {1, 2, 3} as u32 and the
// twelve bytes that encode it are the same twelve bytes. This is the single
// most important thing to know before trusting Guess: it recovers structure,
// never types.
func TestGuessCannotSeeElementWidth(t *testing.T) {
	if got := rootOf(t).Guess(8); got != KindBytes {
		t.Errorf("Guess(8) = %v, want %v (see the doc comment before changing this)", got, KindBytes)
	}
}

func TestLooksRoaring(t *testing.T) {
	tests := []struct {
		name string
		in   []byte
		want bool
	}{
		{"plain bitmap cookie 12346", []byte{0x3a, 0x30, 0x00, 0x00, 1, 0, 0, 0}, true},
		{"run container cookie 12347", []byte{0x3b, 0x30, 0x02, 0x00, 1, 0, 0, 0}, true},
		{"not roaring", []byte{1, 2, 3, 4, 5, 6, 7, 8}, false},
		{"too short", []byte{0x3a, 0x30, 0x00, 0x00}, false},
		{"empty", nil, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := LooksRoaring(tt.in); got != tt.want {
				t.Errorf("LooksRoaring(%v) = %v, want %v", tt.in, got, tt.want)
			}
		})
	}
}
