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
