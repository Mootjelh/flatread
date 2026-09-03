package flatread

import (
	"encoding/binary"
	"math"
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

// --- size prefix, file identifier, string vectors ----------------------------

// sizePrefixed wraps the sample in the layout gRPC and multi-message files use:
// a uint32 byte count, then an ordinary buffer.
func sizePrefixed(inner []byte) []byte {
	out := make([]byte, 4+len(inner))
	binary.LittleEndian.PutUint32(out, uint32(len(inner)))
	copy(out[4:], inner)
	return out
}

func TestRootSizePrefixed(t *testing.T) {
	root, err := RootSizePrefixed(sizePrefixed(sample()))
	if err != nil {
		t.Fatalf("RootSizePrefixed: %v", err)
	}
	if got := root.String(6); got != "hello" {
		t.Errorf("String(6) = %q, want %q", got, "hello")
	}
}

// A size-prefixed buffer taken off a stream can carry the next message behind
// it, so trailing bytes must not be an error.
func TestRootSizePrefixedIgnoresTrailingBytes(t *testing.T) {
	buf := append(sizePrefixed(sample()), make([]byte, 64)...)

	root, err := RootSizePrefixed(buf)
	if err != nil {
		t.Fatalf("RootSizePrefixed: %v", err)
	}
	if got := root.String(6); got != "hello" {
		t.Errorf("String(6) = %q, want %q", got, "hello")
	}
}

func TestRootSizePrefixedRejectsBadPrefixes(t *testing.T) {
	tests := []struct {
		name string
		buf  []byte
	}{
		{"too small", make([]byte, 8)},
		{"prefix larger than the buffer", func() []byte {
			b := make([]byte, 32)
			binary.LittleEndian.PutUint32(b, 9999)
			return b
		}()},
		{"prefix too small to hold a table", func() []byte {
			b := make([]byte, 32)
			binary.LittleEndian.PutUint32(b, 4)
			return b
		}()},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := RootSizePrefixed(tt.buf); err == nil {
				t.Error("expected an error")
			}
		})
	}
}

func TestIsSizePrefixed(t *testing.T) {
	if !IsSizePrefixed(sizePrefixed(sample())) {
		t.Error("IsSizePrefixed(size-prefixed) = false, want true")
	}
	if IsSizePrefixed(sample()) {
		t.Error("IsSizePrefixed(plain) = true, want false")
	}
	if IsSizePrefixed([]byte{1, 2, 3}) {
		t.Error("IsSizePrefixed(tiny) = true, want false")
	}
}

func TestFileIdentifier(t *testing.T) {
	// Bytes 4..8 sit between the root offset and the first vtable, and the
	// sample leaves them empty, which is what a schema with no file_identifier
	// looks like.
	if id, ok := FileIdentifier(sample()); ok {
		t.Errorf("FileIdentifier(sample) = (%q, true), want ok=false", id)
	}

	withID := append([]byte(nil), sample()...)
	copy(withID[4:8], "FLTR")

	id, ok := FileIdentifier(withID)
	if !ok || id != "FLTR" {
		t.Errorf("FileIdentifier = (%q, %v), want (\"FLTR\", true)", id, ok)
	}

	// Writing the identifier must not disturb anything else.
	root, err := Root(withID)
	if err != nil {
		t.Fatalf("Root: %v", err)
	}
	if got := root.String(6); got != "hello" {
		t.Errorf("String(6) = %q after writing an identifier, want %q", got, "hello")
	}
}

// stringVectorSample is its own fixture rather than another slot on sample(),
// so that adding it cannot shift the offsets the other tests assert on.
//
//	 0  u32 root offset -> 32
//	 8  vtable: size 6, so slot 4 only
//	32  table
//	48  vector of 2 string offsets
//	60  "aa"
//	72  "bbb"
func stringVectorSample() []byte {
	b := make([]byte, 80)
	u32 := func(at int, v uint32) { binary.LittleEndian.PutUint32(b[at:], v) }
	u16 := func(at int, v uint16) { binary.LittleEndian.PutUint16(b[at:], v) }

	u32(0, 32)
	u16(8, 6)
	u16(10, 8)
	u16(12, 4)

	u32(32, 32-8)
	u32(36, 48-36)

	u32(48, 2)
	u32(52, 60-52)
	u32(56, 72-56)

	u32(60, 2)
	copy(b[64:], "aa")
	u32(72, 3)
	copy(b[76:], "bbb")

	return b
}

func TestStringVector(t *testing.T) {
	root, err := Root(stringVectorSample())
	if err != nil {
		t.Fatalf("Root: %v", err)
	}

	got := root.StringVector(4)
	want := []string{"aa", "bbb"}
	if len(got) != len(want) {
		t.Fatalf("StringVector(4) = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("StringVector(4) = %v, want %v", got, want)
		}
	}

	if got := root.StringVector(6); got != nil {
		t.Errorf("StringVector(6) = %v, want nil for an absent field", got)
	}
}

// --- vectors of structs ------------------------------------------------------

// structVectorSample holds a vector of 3 structs of 8 bytes each: {1,2} {3,4}
// {5,6}. Structs are stored inline, so there are no offsets anywhere in the
// vector.
//
//	 0  u32 root offset -> 24
//	 8  vtable: size 6, so slot 4 only
//	24  table
//	40  vector of 3 structs, 8 bytes each
func structVectorSample() []byte {
	b := make([]byte, 72)
	u32 := func(at int, v uint32) { binary.LittleEndian.PutUint32(b[at:], v) }
	u16 := func(at int, v uint16) { binary.LittleEndian.PutUint16(b[at:], v) }

	u32(0, 24)
	u16(8, 6)
	u16(10, 8)
	u16(12, 4)

	u32(24, 24-8)
	u32(28, 40-28)

	u32(40, 3)
	u32(44, 1)
	u32(48, 2)
	u32(52, 3)
	u32(56, 4)
	u32(60, 5)
	u32(64, 6)

	return b
}

func TestStructAt(t *testing.T) {
	root, err := Root(structVectorSample())
	if err != nil {
		t.Fatalf("Root: %v", err)
	}

	if got := root.StructVectorLen(4, 8); got != 3 {
		t.Fatalf("StructVectorLen(4, 8) = %d, want 3", got)
	}

	want := [][2]uint32{{1, 2}, {3, 4}, {5, 6}}
	for i, w := range want {
		raw := root.StructAt(4, i, 8)
		if len(raw) != 8 {
			t.Fatalf("StructAt(4, %d, 8) returned %d bytes, want 8", i, len(raw))
		}
		x := binary.LittleEndian.Uint32(raw)
		y := binary.LittleEndian.Uint32(raw[4:])
		if x != w[0] || y != w[1] {
			t.Errorf("struct %d = {%d, %d}, want {%d, %d}", i, x, y, w[0], w[1])
		}
	}

	for _, bad := range []int{-1, 3, 99} {
		if raw := root.StructAt(4, bad, 8); raw != nil {
			t.Errorf("StructAt(4, %d, 8) = %v, want nil", bad, raw)
		}
	}
	if raw := root.StructAt(4, 0, 0); raw != nil {
		t.Errorf("StructAt with size 0 = %v, want nil", raw)
	}
}

// A wrong element size is caught, because the declared count multiplied by that
// size no longer fits the buffer. VectorLen cannot do this: it does not know
// how wide an element is.
func TestStructVectorLenRejectsAnImpossibleSize(t *testing.T) {
	root, err := Root(structVectorSample())
	if err != nil {
		t.Fatalf("Root: %v", err)
	}
	if got := root.StructVectorLen(4, 64); got != 0 {
		t.Errorf("StructVectorLen(4, 64) = %d, want 0", got)
	}
	if raw := root.StructAt(4, 0, 64); raw != nil {
		t.Errorf("StructAt(4, 0, 64) = %v, want nil", raw)
	}
}

// --- probing spread across a vector ------------------------------------------

// headResolvesSample is built so that the FIRST FOUR elements of a vector each
// resolve to a plausible vtable by coincidence, while later ones do not.
//
// This is the case that made Guess report vector-of-tables on data that is
// nothing of the kind. Probing elements 0..3 sees four hits and concludes;
// probing 0, 2, 4 and 7 hits the wall at element 4.
//
//	 0  u32 root offset -> 32
//	 8  vtable, shared by the root table and the decoy at 88
//	32  root table
//	48  vector of 8 uint32
//	88  decoy: a position that passes plausibleTable
func headResolvesSample() []byte {
	b := make([]byte, 96)
	u32 := func(at int, v uint32) { binary.LittleEndian.PutUint32(b[at:], v) }
	u16 := func(at int, v uint16) { binary.LittleEndian.PutUint16(b[at:], v) }

	u32(0, 32)
	u16(8, 6)
	u16(10, 8)
	u16(12, 4)

	u32(32, 32-8)
	u32(36, 48-36)

	u32(48, 8)
	// Elements 0..3 point at the decoy at 88.
	u32(52, 88-52)
	u32(56, 88-56)
	u32(60, 88-60)
	u32(64, 88-64)
	// Elements 4..7 point just past themselves, at nothing in particular.
	u32(68, 1)
	u32(72, 1)
	u32(76, 1)
	u32(80, 1)

	u32(88, 88-8)

	return b
}

func TestGuessProbesAcrossTheWholeVector(t *testing.T) {
	root, err := Root(headResolvesSample())
	if err != nil {
		t.Fatalf("Root: %v", err)
	}

	// Control: the first four elements really do all resolve, which is what
	// made the old head-only probe conclude vector-of-tables.
	for i := 0; i < 4; i++ {
		if _, ok := root.TableAt(4, i); !ok {
			t.Fatalf("fixture is wrong: element %d does not resolve", i)
		}
	}

	if got := root.Guess(4); got != KindBytes {
		t.Errorf("Guess(4) = %v, want %v — probing only the head of a vector is what this guards against", got, KindBytes)
	}
}

func TestProbeIndices(t *testing.T) {
	tests := []struct {
		n, max uint32
		want   []uint32
	}{
		{0, 4, nil},
		{1, 4, []uint32{0}},
		{4, 4, []uint32{0, 1, 2, 3}},
		{8, 4, []uint32{0, 2, 4, 7}},
		{100, 4, []uint32{0, 33, 66, 99}},
		{8, 1, []uint32{0}},
		{8, 0, nil},
	}
	for _, tt := range tests {
		got := probeIndices(tt.n, tt.max)
		if len(got) != len(tt.want) {
			t.Errorf("probeIndices(%d, %d) = %v, want %v", tt.n, tt.max, got, tt.want)
			continue
		}
		for i := range tt.want {
			if got[i] != tt.want[i] {
				t.Errorf("probeIndices(%d, %d) = %v, want %v", tt.n, tt.max, got, tt.want)
				break
			}
		}
		// The last element must always be probed, or the guard does not guard.
		if tt.n > 0 && tt.max > 1 && got[len(got)-1] != tt.n-1 {
			t.Errorf("probeIndices(%d, %d) does not probe the last element", tt.n, tt.max)
		}
	}
}

// floatVectorSample: slot 4 is a vector of 3 float32, slot 6 a vector of 2
// float64. Every value is exactly representable, so a decode error shows up as
// a wrong number rather than a rounding argument.
//
//	 0  u32 root offset -> 24
//	 8  vtable: size 8, so slots 4 and 6
//	24  table
//	40  float32 vector
//	56  float64 vector
func floatVectorSample() []byte {
	b := make([]byte, 80)
	u32 := func(at int, v uint32) { binary.LittleEndian.PutUint32(b[at:], v) }
	u16 := func(at int, v uint16) { binary.LittleEndian.PutUint16(b[at:], v) }
	f32 := func(at int, v float32) { u32(at, math.Float32bits(v)) }
	f64 := func(at int, v float64) { binary.LittleEndian.PutUint64(b[at:], math.Float64bits(v)) }

	u32(0, 24)
	u16(8, 8)
	u16(10, 12)
	u16(12, 4)
	u16(14, 8)

	u32(24, 24-8)
	u32(28, 40-28)
	u32(32, 56-32)

	u32(40, 3)
	f32(44, 1.5)
	f32(48, -2.25)
	f32(52, 3.75)

	u32(56, 2)
	f64(60, 1.25)
	f64(68, -4.5)

	return b
}

func TestFloatVectors(t *testing.T) {
	root, err := Root(floatVectorSample())
	if err != nil {
		t.Fatalf("Root: %v", err)
	}

	got32 := root.Float32Vector(4)
	want32 := []float32{1.5, -2.25, 3.75}
	if len(got32) != len(want32) {
		t.Fatalf("Float32Vector(4) = %v, want %v", got32, want32)
	}
	for i := range want32 {
		if got32[i] != want32[i] {
			t.Errorf("Float32Vector(4)[%d] = %v, want %v", i, got32[i], want32[i])
		}
	}

	got64 := root.Float64Vector(6)
	want64 := []float64{1.25, -4.5}
	if len(got64) != len(want64) {
		t.Fatalf("Float64Vector(6) = %v, want %v", got64, want64)
	}
	for i := range want64 {
		if got64[i] != want64[i] {
			t.Errorf("Float64Vector(6)[%d] = %v, want %v", i, got64[i], want64[i])
		}
	}

	if got := root.Float32Vector(8); got != nil {
		t.Errorf("Float32Vector(8) = %v, want nil for an absent field", got)
	}
	if got := root.Float64Vector(8); got != nil {
		t.Errorf("Float64Vector(8) = %v, want nil for an absent field", got)
	}
}

// unionSample has a single union in slots 4 and 6, and a vector of unions in
// slots 8 and 10. The third element of that vector carries tag 0, which is a
// hole in the middle rather than the end.
//
//	 0  u32 root offset -> 40
//	 8  root vtable: slots 4, 6, 8, 10
//	24  vtable shared by every value table
//	40  root table
//	64  the single union's value {111}
//	72  tag vector {1, 2, 0}
//	80  value vector -> 96, 104, 112
func unionSample() []byte {
	b := make([]byte, 120)
	u32 := func(at int, v uint32) { binary.LittleEndian.PutUint32(b[at:], v) }
	u16 := func(at int, v uint16) { binary.LittleEndian.PutUint16(b[at:], v) }

	u32(0, 40)

	u16(8, 12)
	u16(10, 20)
	u16(12, 4)  // slot 4  tag
	u16(14, 8)  // slot 6  value
	u16(16, 12) // slot 8  tag vector
	u16(18, 16) // slot 10 value vector

	// One vtable for every value table: they all have the same shape.
	u16(24, 6)
	u16(26, 8)
	u16(28, 4)

	u32(40, 40-8)
	u32(44, 2)     // the tag, read as a single byte
	u32(48, 64-48) // -> value table
	u32(52, 72-52) // -> tag vector
	u32(56, 80-56) // -> value vector

	u32(64, 64-24)
	u32(68, 111)

	u32(72, 3)
	b[76], b[77], b[78] = 1, 2, 0

	u32(80, 3)
	u32(84, 96-84)
	u32(88, 104-88)
	u32(92, 112-92)

	u32(96, 96-24)
	u32(100, 201)
	u32(104, 104-24)
	u32(108, 202)
	u32(112, 112-24)
	u32(116, 203)

	return b
}

func TestUnion(t *testing.T) {
	root, err := Root(unionSample())
	if err != nil {
		t.Fatalf("Root: %v", err)
	}

	if got := root.UnionType(4); got != 2 {
		t.Errorf("UnionType(4) = %d, want 2", got)
	}

	kind, value, ok := root.Union(4)
	if !ok {
		t.Fatal("Union(4) reported absent")
	}
	if kind != 2 {
		t.Errorf("kind = %d, want 2", kind)
	}
	if got := value.Uint32(4); got != 111 {
		t.Errorf("value.Uint32(4) = %d, want 111", got)
	}
}

func TestUnionVector(t *testing.T) {
	root, err := Root(unionSample())
	if err != nil {
		t.Fatalf("Root: %v", err)
	}

	if got := root.UnionVectorLen(8); got != 3 {
		t.Fatalf("UnionVectorLen(8) = %d, want 3", got)
	}

	for i, want := range []struct {
		kind  byte
		value uint32
	}{{1, 201}, {2, 202}} {
		kind, value, ok := root.UnionAt(8, i)
		if !ok {
			t.Fatalf("UnionAt(8, %d) reported absent", i)
		}
		if kind != want.kind {
			t.Errorf("element %d: kind = %d, want %d", i, kind, want.kind)
		}
		if got := value.Uint32(4); got != want.value {
			t.Errorf("element %d: value = %d, want %d", i, got, want.value)
		}
	}
}

// A tag of 0 is a hole, not the end of the vector. Stopping there would skip
// every element after it.
func TestUnionNoneIsAHoleNotTheEnd(t *testing.T) {
	root, err := Root(unionSample())
	if err != nil {
		t.Fatalf("Root: %v", err)
	}

	if _, _, ok := root.UnionAt(8, 2); ok {
		t.Error("UnionAt(8, 2) should report absent: its tag is UnionNone")
	}
	if got := root.UnionVectorLen(8); got != 3 {
		t.Errorf("UnionVectorLen(8) = %d, want 3: the hole still counts", got)
	}
}

func TestUnionOutOfRange(t *testing.T) {
	root, err := Root(unionSample())
	if err != nil {
		t.Fatalf("Root: %v", err)
	}

	for _, i := range []int{-1, 3, 1 << 20} {
		if _, _, ok := root.UnionAt(8, i); ok {
			t.Errorf("UnionAt(8, %d) should be out of range", i)
		}
	}
	// Slot 14 holds nothing at all.
	if _, _, ok := root.Union(14); ok {
		t.Error("Union(14) should report absent")
	}
	if got := root.UnionVectorLen(14); got != 0 {
		t.Errorf("UnionVectorLen(14) = %d, want 0", got)
	}
}

func TestUnionHint(t *testing.T) {
	root, err := Root(unionSample())
	if err != nil {
		t.Fatalf("Root: %v", err)
	}

	single, ok := root.UnionHint(4)
	if !ok {
		t.Fatal("UnionHint(4) found nothing, want a single union")
	}
	if single.ValueSlot != 6 || single.Elements != 0 {
		t.Errorf("UnionHint(4) = %+v, want value slot 6 and 0 elements", single)
	}

	vec, ok := root.UnionHint(8)
	if !ok {
		t.Fatal("UnionHint(8) found nothing, want a vector of unions")
	}
	if vec.ValueSlot != 10 || vec.Elements != 3 {
		t.Errorf("UnionHint(8) = %+v, want value slot 10 and 3 elements", vec)
	}
}

// The ordinary fixture has no unions in it. A hint here would be a false
// positive, which is worse than no hint: it would send someone reading an
// unknown payload after a pairing that does not exist.
func TestUnionHintDoesNotFireOnOrdinaryFields(t *testing.T) {
	r := rootOf(t)

	for _, slot := range []uint16{4, 6, 8, 10, 12} {
		if shape, ok := r.UnionHint(slot); ok {
			t.Errorf("UnionHint(%d) = %+v, want no hint", slot, shape)
		}
	}
}

// nestedNearItsVtable builds a root whose only field is a nested table sitting
// close to its own vtable, so the soffset is small.
//
// That is what a real encoder produces and it is the case Guess used to get
// wrong. The soffset is read where a vector keeps its length, and a small one
// fits the buffer, so the length reading wins and the table is reported as a
// short byte vector. sample() above avoids this by accident: its nested table
// is far enough from its vtable that the implied length runs off the end.
//
//	0   root uoffset
//	4   root vtable      size 6, table 8, slot 4 at +4
//	12  root table       soffset 8, then the offset to the nested table
//	20  nested vtable    size 8, table 12, slots at +4 and +8
//	28  nested table     soffset 8, then two u32 fields
func nestedNearItsVtable() []byte {
	b := make([]byte, 40)
	u32 := func(at int, v uint32) { binary.LittleEndian.PutUint32(b[at:], v) }
	u16 := func(at int, v uint16) { binary.LittleEndian.PutUint16(b[at:], v) }

	u32(0, 12)

	u16(4, 6)
	u16(6, 8)
	u16(8, 4)

	u32(12, uint32(12-4))
	u32(16, uint32(28-16))

	u16(20, 8)
	u16(22, 12)
	u16(24, 4)
	u16(26, 8)

	u32(28, uint32(28-20))
	u32(32, 7)
	u32(36, 9)

	return b
}

// TestGuessFindsANestedTableWithASmallSoffset is the regression for #5.
//
// The control comes first. Without it the assertion below can pass for the
// wrong reason: if the fixture were malformed enough that the length reading
// never applied, Guess would reach the table check the old way and the test
// would say nothing about the bug.
func TestGuessFindsANestedTableWithASmallSoffset(t *testing.T) {
	buf := nestedNearItsVtable()

	// Control: the length reading really does apply here, which is what makes
	// this the hard case. The soffset is 8 and the payload it implies is inside
	// the buffer.
	const nested = 28
	if n := binary.LittleEndian.Uint32(buf[nested:]); n != 8 || nested+4+int(n) > len(buf) {
		t.Fatalf("fixture does not exercise the bug: implied length %d at %d in a %d-byte buffer", n, nested, len(buf))
	}

	root, err := Root(buf)
	if err != nil {
		t.Fatalf("Root: %v", err)
	}

	if got := root.Guess(4); got != KindTable {
		t.Errorf("Guess(4) = %v, want %v: the soffset was read as a vector length", got, KindTable)
	}

	// And it has to be readable as one, not merely labelled.
	sub, ok := root.Table(4)
	if !ok {
		t.Fatal("Table(4) did not resolve")
	}
	if got := sub.Uint32(4); got != 7 {
		t.Errorf("nested slot 4 = %d, want 7", got)
	}
}

// TestGuessStillCallsAShortByteVectorBytes is the other half of #5 and the one
// that matters if the table check is ever loosened.
//
// Preferring a table costs false positives on short vectors, since a handful of
// arbitrary bytes can read as a small vtable. A vector that does not look like
// a table has to keep reading as bytes.
func TestGuessStillCallsAShortByteVectorBytes(t *testing.T) {
	b := make([]byte, 40)
	u32 := func(at int, v uint32) { binary.LittleEndian.PutUint32(b[at:], v) }
	u16 := func(at int, v uint16) { binary.LittleEndian.PutUint16(b[at:], v) }

	u32(0, 12)
	u16(4, 6)
	u16(6, 8)
	u16(8, 4)
	u32(12, uint32(12-4))
	u32(16, uint32(24-16))

	// A 12-byte payload with a zero in it, so it is neither printable nor
	// roaring, and whose leading word points nowhere a vtable could be.
	u32(24, 12)
	u32(28, 0x00010203)
	u32(32, 0x00040506)
	u32(36, 0x00070809)

	root, err := Root(b)
	if err != nil {
		t.Fatalf("Root: %v", err)
	}
	if got := root.Guess(4); got != KindBytes {
		t.Errorf("Guess(4) = %v, want %v: a byte vector was taken for a table", got, KindBytes)
	}
}
