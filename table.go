// Package flatread reads FlatBuffers buffers for which you have no schema.
//
// The normal FlatBuffers workflow compiles a .fbs schema into generated
// accessors, and field names live in that generated code. When you are handed
// a binary payload without its schema (reverse engineering a protocol,
// inspecting a capture, triaging a corrupt file) there is nothing to generate
// from, and the standard library is of no help.
//
// This package reads a buffer positionally instead: fields are addressed by
// their vtable slot number rather than by name. That is enough to walk an
// unknown buffer, and enough to write a decoder once you have worked out which
// slot means what.
//
// # Buffer layout
//
// Everything is little-endian. Reading the code below is much easier with this
// to hand:
//
//	buf[0:4]                 uoffset to the root table
//
//	table[-soffset]          the table's vtable (soffset is a SIGNED int32
//	                         stored at the table's own position)
//	vtable[0:2]              vtable size in bytes
//	vtable[2:4]              inline table size in bytes
//	vtable[4+2*i : 6+2*i]    byte offset of field i inside the table,
//	                         0 meaning the field is absent
//
// So field i lives at slot number 4+2*i: the first field is slot 4, the second
// slot 6, and so on. Slot numbers, not indices, are what you pass here.
//
// Offsets to strings, vectors and nested tables are uoffsets: unsigned, and
// relative to the position of the offset itself.
//
// # Safety
//
// Every accessor is bounds-checked and returns a zero value rather than
// panicking. That is deliberate: this package exists to be pointed at bytes
// nobody has validated, so malformed input is the expected case and not an
// exceptional one. The fuzz test in this package asserts it.
//
// The cost of that choice is that a zero value is ambiguous. It means either
// "the field holds zero" or "there is no such field". Use [Table.Has] when the
// difference matters.
package flatread

import (
	"encoding/binary"
	"fmt"
	"math"
)

// Table is a FlatBuffers table at a position inside a buffer. The zero Table is
// not usable; get one from [Root].
//
// A Table is a view, not a copy: it holds a reference to the underlying buffer
// and no accessor allocates unless it must ([Table.String] and the vector
// accessors do; the scalar ones do not).
type Table struct {
	buf []byte
	pos uint32
}

// Root returns the root table of a FlatBuffers buffer.
//
// It fails only when the buffer is too small to hold a root offset, or when
// that offset points outside the buffer. It does NOT validate the rest of the
// buffer. That is what the accessors' bounds checks are for.
func Root(buf []byte) (Table, error) {
	if len(buf) < 8 {
		return Table{}, fmt.Errorf("flatread: buffer too small (%d bytes, need at least 8)", len(buf))
	}
	root := binary.LittleEndian.Uint32(buf)
	if int(root)+4 > len(buf) {
		return Table{}, fmt.Errorf("flatread: root offset %d is outside a %d-byte buffer", root, len(buf))
	}
	return Table{buf: buf, pos: root}, nil
}

// RootSizePrefixed returns the root table of a size-prefixed buffer.
//
// The size-prefixed layout puts a uint32 byte count in front of an otherwise
// ordinary buffer, so that a reader taking bytes off a stream knows where one
// message ends. gRPC uses it, and so does anything that stores several buffers
// back to back in one file.
//
// Passing such a buffer to [Root] does not fail loudly. Root reads the size as
// the root offset, and what happens next depends on the number: usually an
// error, sometimes a table full of nonsense. If you do not know which layout
// you have, [IsSizePrefixed] guesses.
//
// Trailing bytes past the declared size are ignored, so this can be called on a
// stream buffer that holds more than one message.
func RootSizePrefixed(buf []byte) (Table, error) {
	if len(buf) < 12 {
		return Table{}, fmt.Errorf("flatread: buffer too small for a size prefix (%d bytes, need at least 12)", len(buf))
	}
	size := binary.LittleEndian.Uint32(buf)
	if int(size)+4 > len(buf) {
		return Table{}, fmt.Errorf("flatread: size prefix claims %d bytes but only %d follow", size, len(buf)-4)
	}
	if size < 8 {
		return Table{}, fmt.Errorf("flatread: size prefix of %d is too small to hold a table", size)
	}
	return Root(buf[4 : 4+size])
}

// IsSizePrefixed guesses whether buf carries a size prefix, by checking whether
// its first uint32 accounts for the rest of the buffer exactly.
//
// Like [Table.Guess] this reads structure rather than a recorded fact, and it
// can be fooled: a plain buffer whose root offset happens to equal len(buf)-4
// looks identical. It is a starting point for an unknown payload, not something
// to branch on in production once you know the format.
func IsSizePrefixed(buf []byte) bool {
	if len(buf) < 12 {
		return false
	}
	return int(binary.LittleEndian.Uint32(buf))+4 == len(buf)
}

// FileIdentifier reads the optional four-byte identifier a schema can attach to
// its buffers, which sits directly after the root offset.
//
// The bool reports whether those four bytes look like an identifier at all,
// meaning printable ASCII with no spaces. Nothing in the buffer records whether
// the field is present: a schema without a file_identifier puts padding or the
// start of a vtable in the same place. So a false here means "these bytes are
// not a plausible identifier", not "this schema has none".
//
// When it is present it is usually the fastest way to work out what you have
// been handed, because it is chosen to be human-readable and often appears in
// the schema, the docs or the code that produced the buffer.
func FileIdentifier(buf []byte) (string, bool) {
	if len(buf) < 8 {
		return "", false
	}
	id := buf[4:8]
	for _, c := range id {
		if c <= 0x20 || c > 0x7e {
			return string(id), false
		}
	}
	return string(id), true
}

// TableAtOffset returns the table at an absolute byte position in buf.
//
// Use it when you already know where a table starts, for instance when a
// size-prefixed buffer puts the root somewhere other than offset 0, or when
// you are re-entering a buffer at a position [Table.Pos] gave you earlier.
func TableAtOffset(buf []byte, pos uint32) (Table, error) {
	if int(pos)+4 > len(buf) {
		return Table{}, fmt.Errorf("flatread: position %d is outside a %d-byte buffer", pos, len(buf))
	}
	return Table{buf: buf, pos: pos}, nil
}

// Pos is the table's absolute byte position in the buffer.
func (t Table) Pos() uint32 { return t.pos }

// Buffer returns the underlying buffer. It is not copied.
func (t Table) Buffer() []byte { return t.buf }

func (t Table) u8(p uint32) uint8 {
	if int(p) >= len(t.buf) {
		return 0
	}
	return t.buf[p]
}

func (t Table) u16(p uint32) uint16 {
	if int(p)+2 > len(t.buf) {
		return 0
	}
	return binary.LittleEndian.Uint16(t.buf[p:])
}

func (t Table) u32(p uint32) uint32 {
	if int(p)+4 > len(t.buf) {
		return 0
	}
	return binary.LittleEndian.Uint32(t.buf[p:])
}

func (t Table) u64(p uint32) uint64 {
	if int(p)+8 > len(t.buf) {
		return 0
	}
	return binary.LittleEndian.Uint64(t.buf[p:])
}

// vtable returns the position of this table's vtable, and whether it is
// readable at all.
func (t Table) vtable() (uint32, bool) {
	soffset := int32(t.u32(t.pos))
	vt := uint32(int32(t.pos) - soffset)
	if int(vt)+4 > len(t.buf) {
		return 0, false
	}
	return vt, true
}

// offset resolves a vtable slot to an absolute position inside the table, or 0
// when the field is absent.
func (t Table) offset(slot uint16) uint32 {
	// Slots are the byte offsets FlatBuffers uses: 4, 6, 8, … Anything below 4
	// would index the vtable's own size header, and an odd value would read
	// across two entries. Both are caller errors that would otherwise return
	// convincing nonsense.
	if slot < 4 || slot%2 != 0 {
		return 0
	}
	vt, ok := t.vtable()
	if !ok {
		return 0
	}
	if slot >= t.u16(vt) {
		return 0
	}
	off := t.u16(vt + uint32(slot))
	if off == 0 {
		return 0
	}
	return t.pos + uint32(off)
}

// Slots lists the vtable slots this table actually populates, ascending.
//
// This is the entry point for exploring an unknown buffer: it tells you which
// fields are present without knowing what any of them mean.
func (t Table) Slots() []uint16 {
	vt, ok := t.vtable()
	if !ok {
		return nil
	}
	size := t.u16(vt)
	if int(vt)+int(size) > len(t.buf) {
		return nil
	}
	var out []uint16
	for slot := uint16(4); slot < size; slot += 2 {
		if t.u16(vt+uint32(slot)) != 0 {
			out = append(out, slot)
		}
	}
	return out
}

// Has reports whether the field is present, as distinct from present and zero.
//
// FlatBuffers omits fields equal to their default, so a writer that stored 0
// may well have written nothing at all. Has cannot recover that distinction.
// It reports what is in the buffer, which is the most anyone can know.
func (t Table) Has(slot uint16) bool { return t.offset(slot) != 0 }

// --- scalars ----------------------------------------------------------------

// Byte reads a uint8 field.
func (t Table) Byte(slot uint16) uint8 {
	if p := t.offset(slot); p != 0 {
		return t.u8(p)
	}
	return 0
}

// Bool reads a bool field. FlatBuffers stores these as a single byte.
func (t Table) Bool(slot uint16) bool { return t.Byte(slot) != 0 }

// Int8 reads an int8 field.
func (t Table) Int8(slot uint16) int8 { return int8(t.Byte(slot)) }

// Uint16 reads a uint16 field.
func (t Table) Uint16(slot uint16) uint16 {
	if p := t.offset(slot); p != 0 {
		return t.u16(p)
	}
	return 0
}

// Int16 reads an int16 field.
func (t Table) Int16(slot uint16) int16 { return int16(t.Uint16(slot)) }

// Uint32 reads a uint32 field.
func (t Table) Uint32(slot uint16) uint32 {
	if p := t.offset(slot); p != 0 {
		return t.u32(p)
	}
	return 0
}

// Int32 reads an int32 field.
func (t Table) Int32(slot uint16) int32 { return int32(t.Uint32(slot)) }

// Uint64 reads a uint64 field.
func (t Table) Uint64(slot uint16) uint64 {
	if p := t.offset(slot); p != 0 {
		return t.u64(p)
	}
	return 0
}

// Int64 reads an int64 field.
func (t Table) Int64(slot uint16) int64 { return int64(t.Uint64(slot)) }

// Float32 reads a float32 field.
func (t Table) Float32(slot uint16) float32 { return math.Float32frombits(t.Uint32(slot)) }

// Float64 reads a float64 field.
func (t Table) Float64(slot uint16) float64 { return math.Float64frombits(t.Uint64(slot)) }

// --- strings and byte vectors -----------------------------------------------

// String reads a string field. FlatBuffers strings are length-prefixed and
// NUL-terminated; the terminator is not included.
//
// The bytes are not validated as UTF-8, because a schema-less buffer is exactly
// where a "string" often turns out to be something else.
func (t Table) String(slot uint16) string {
	return string(t.Bytes(slot))
}

// Bytes reads a vector of bytes, returned as a sub-slice of the buffer without
// copying. Do not retain it past the lifetime of the buffer, and do not write
// to it.
func (t Table) Bytes(slot uint16) []byte {
	p := t.offset(slot)
	if p == 0 {
		return nil
	}
	start := p + t.u32(p)
	n := t.u32(start)
	from, to := int(start)+4, int(start)+4+int(n)
	if n == 0 || to > len(t.buf) || from > to {
		return nil
	}
	return t.buf[from:to]
}

// --- vectors ----------------------------------------------------------------

// VectorLen reports the element count of a vector field, of any element type.
func (t Table) VectorLen(slot uint16) int {
	p := t.offset(slot)
	if p == 0 {
		return 0
	}
	vec := p + t.u32(p)
	if int(vec)+4 > len(t.buf) {
		return 0
	}
	return int(t.u32(vec))
}

// vectorStart returns the position of a vector's first element and its length.
func (t Table) vectorStart(slot uint16, elemSize int) (uint32, int, bool) {
	p := t.offset(slot)
	if p == 0 {
		return 0, 0, false
	}
	vec := p + t.u32(p)
	if int(vec)+4 > len(t.buf) {
		return 0, 0, false
	}
	n := int(t.u32(vec))
	if n < 0 || int(vec)+4+n*elemSize > len(t.buf) {
		return 0, 0, false
	}
	return vec + 4, n, true
}

// Int32Vector reads a vector of int32.
func (t Table) Int32Vector(slot uint16) []int32 {
	first, n, ok := t.vectorStart(slot, 4)
	if !ok {
		return nil
	}
	out := make([]int32, n)
	for i := 0; i < n; i++ {
		out[i] = int32(t.u32(first + uint32(i)*4))
	}
	return out
}

// Uint32Vector reads a vector of uint32.
func (t Table) Uint32Vector(slot uint16) []uint32 {
	first, n, ok := t.vectorStart(slot, 4)
	if !ok {
		return nil
	}
	out := make([]uint32, n)
	for i := 0; i < n; i++ {
		out[i] = t.u32(first + uint32(i)*4)
	}
	return out
}

// StructAt returns the bytes of element i in a vector of structs, without
// copying.
//
// FlatBuffers structs are not tables. They are fixed-size records stored inline
// in the vector, one after another, with no vtable and no offsets. That makes
// them unreadable through [Table.TableAt], which treats every element as an
// offset: on a struct vector it adds a number that was never an offset to the
// element position and hands back a Table pointing at whatever sits there.
//
// The element size comes from the schema. Nothing in the buffer records it,
// which is also why [Table.Guess] cannot tell a struct vector from a byte
// vector and reports [KindBytes] for both.
//
// Fields inside the returned bytes are at fixed offsets, again from the schema,
// and are read with encoding/binary directly.
func (t Table) StructAt(slot uint16, i, size int) []byte {
	if size <= 0 || i < 0 {
		return nil
	}
	first, n, ok := t.vectorStart(slot, size)
	if !ok || i >= n {
		return nil
	}
	from := int(first) + i*size
	to := from + size
	if from < 0 || to > len(t.buf) || from > to {
		return nil
	}
	return t.buf[from:to]
}

// StructVectorLen reports how many structs of the given size a vector holds,
// or 0 when the declared count does not fit the buffer.
//
// This differs from [Table.VectorLen], which trusts the count without knowing
// the element width. A truncated buffer shows up here and not there.
func (t Table) StructVectorLen(slot uint16, size int) int {
	if size <= 0 {
		return 0
	}
	_, n, ok := t.vectorStart(slot, size)
	if !ok {
		return 0
	}
	return n
}

// StringVector reads a vector of strings.
//
// An element that is out of range comes back as "", the same as [Table.StringAt]
// would give, so the length of the result always matches the vector's declared
// length.
func (t Table) StringVector(slot uint16) []string {
	_, n, ok := t.vectorStart(slot, 4)
	if !ok {
		return nil
	}
	out := make([]string, n)
	for i := 0; i < n; i++ {
		out[i] = t.StringAt(slot, i)
	}
	return out
}

// Float32Vector reads a vector of float32.
func (t Table) Float32Vector(slot uint16) []float32 {
	first, n, ok := t.vectorStart(slot, 4)
	if !ok {
		return nil
	}
	out := make([]float32, n)
	for i := 0; i < n; i++ {
		out[i] = math.Float32frombits(t.u32(first + uint32(i)*4))
	}
	return out
}

// Float64Vector reads a vector of float64.
func (t Table) Float64Vector(slot uint16) []float64 {
	first, n, ok := t.vectorStart(slot, 8)
	if !ok {
		return nil
	}
	out := make([]float64, n)
	for i := 0; i < n; i++ {
		out[i] = math.Float64frombits(t.u64(first + uint32(i)*8))
	}
	return out
}

// Int64Vector reads a vector of int64.
func (t Table) Int64Vector(slot uint16) []int64 {
	first, n, ok := t.vectorStart(slot, 8)
	if !ok {
		return nil
	}
	out := make([]int64, n)
	for i := 0; i < n; i++ {
		out[i] = int64(t.u64(first + uint32(i)*8))
	}
	return out
}

// --- nested tables ----------------------------------------------------------

// Table reads a nested table field. The bool reports whether it was present and
// in range.
func (t Table) Table(slot uint16) (Table, bool) {
	p := t.offset(slot)
	if p == 0 {
		return Table{}, false
	}
	target := p + t.u32(p)
	if int(target)+4 > len(t.buf) {
		return Table{}, false
	}
	return Table{buf: t.buf, pos: target}, true
}

// TableAt reads element i of a vector of tables.
func (t Table) TableAt(slot uint16, i int) (Table, bool) {
	first, n, ok := t.vectorStart(slot, 4)
	if !ok || i < 0 || i >= n {
		return Table{}, false
	}
	elem := first + uint32(i)*4
	target := elem + t.u32(elem)
	if int(target)+4 > len(t.buf) {
		return Table{}, false
	}
	return Table{buf: t.buf, pos: target}, true
}

// StringAt reads element i of a vector of strings.
func (t Table) StringAt(slot uint16, i int) string {
	return string(t.BytesAt(slot, i))
}

// BytesAt reads element i of a vector of strings or byte vectors, without
// copying.
func (t Table) BytesAt(slot uint16, i int) []byte {
	first, n, ok := t.vectorStart(slot, 4)
	if !ok || i < 0 || i >= n {
		return nil
	}
	elem := first + uint32(i)*4
	start := elem + t.u32(elem)
	sn := t.u32(start)
	from, to := int(start)+4, int(start)+4+int(sn)
	if sn == 0 || to > len(t.buf) || from > to {
		return nil
	}
	return t.buf[from:to]
}

// --- unions -----------------------------------------------------------------

// UnionNone is the type tag a union carries when it holds nothing. FlatBuffers
// reserves 0 for it in every union enum.
const UnionNone byte = 0

// UnionType reads the type tag of a union without resolving its value.
//
// A union in a schema becomes two fields in the table, in consecutive slots:
// the tag at typeSlot, saying which member is present, and the value at
// typeSlot+2. Without the schema the tag is just a number, but it is the number
// the schema's enum is keyed on, so it is what you match against once you know
// what the members are.
func (t Table) UnionType(typeSlot uint16) byte { return t.Byte(typeSlot) }

// Union reads a union: its type tag and the table it points at. The bool is
// false when the tag is [UnionNone] or the value is missing.
//
// Union members are usually tables, which is what this returns. A schema can
// also put a string or a struct in a union; for those, read typeSlot+2 directly
// with [Table.String] or [Table.StructAt], since the value slot is an ordinary
// slot.
func (t Table) Union(typeSlot uint16) (byte, Table, bool) {
	kind := t.Byte(typeSlot)
	if kind == UnionNone {
		return UnionNone, Table{}, false
	}
	value, ok := t.Table(typeSlot + 2)
	return kind, value, ok
}

// UnionVectorLen reports how many elements a vector of unions holds.
//
// A union vector is two parallel vectors: the tags, as a vector of bytes at
// typeSlot, and the values at typeSlot+2. An encoder keeps them the same
// length. This reports the shorter of the two, so any index it accepts is in
// range for both, which matters on a truncated buffer where they are not.
func (t Table) UnionVectorLen(typeSlot uint16) int {
	tags := len(t.Bytes(typeSlot))
	values := t.VectorLen(typeSlot + 2)
	if values < tags {
		return values
	}
	return tags
}

// UnionAt reads element i of a vector of unions.
//
// An element whose tag is [UnionNone] reports false. That is not an error and
// not the end of the vector: a union vector can hold empty slots between full
// ones, so keep walking to [Table.UnionVectorLen].
func (t Table) UnionAt(typeSlot uint16, i int) (byte, Table, bool) {
	if i < 0 || i >= t.UnionVectorLen(typeSlot) {
		return UnionNone, Table{}, false
	}
	kind := t.Bytes(typeSlot)[i]
	if kind == UnionNone {
		return UnionNone, Table{}, false
	}
	value, ok := t.TableAt(typeSlot+2, i)
	return kind, value, ok
}
