package flatread

import (
	"encoding/binary"
)

// Kind is a guess at what a field holds.
//
// FlatBuffers does not record field types in the buffer. The schema carries
// them, and a schema is exactly what you do not have here. So [Table.Guess]
// leans entirely on structure: a length prefix that fits, bytes that are all
// printable, a known magic number, an offset that resolves to a readable
// vtable.
//
// It is right often enough to be the fastest way into an unknown buffer, and it
// is wrong often enough that you should never build a decoder on it. Use it to
// find the slots you care about, confirm each one against real data, then read
// them with the typed accessors.
type Kind int

const (
	// KindScalar is the fallback: an inline number, or a field whose offset
	// resolves to nothing recognisable. Note that KindScalar is what an unknown
	// field degrades to, so it means "no better guess" rather than "definitely
	// a number".
	KindScalar Kind = iota

	// KindString is a length-prefixed run of printable ASCII.
	KindString

	// KindBytes is a length-prefixed run that is not printable and not
	// recognised. A vector of any fixed-width numeric type also lands here, and
	// so does a vector of structs: nothing in the buffer distinguishes a byte
	// vector from an int32 vector or from packed fixed-size records. Read those
	// with [Table.StructAt] once the schema tells you the element size.
	KindBytes

	// KindRoaring is a byte vector carrying a RoaringBitmap serial cookie.
	// Roaring is common in search and analytics payloads, compact enough to be
	// worth recognising, and its cookie is distinctive.
	KindRoaring

	// KindTable is an offset resolving to a plausible vtable.
	KindTable

	// KindVectorOfTables is a vector whose elements all resolve to plausible
	// vtables.
	KindVectorOfTables
)

// String renders the Kind for display.
func (k Kind) String() string {
	switch k {
	case KindString:
		return "string"
	case KindBytes:
		return "bytes"
	case KindRoaring:
		return "roaring"
	case KindTable:
		return "table"
	case KindVectorOfTables:
		return "vector-of-tables"
	default:
		return "scalar"
	}
}

// maxProbeElems caps how many vector elements are checked before deciding it is
// a vector of tables. Four is enough to make a coincidence unlikely and keeps
// the guess cheap on a vector of millions.
const maxProbeElems = 4

// maxProbeVector is the largest element count worth probing. Beyond this a
// "vector" is much more likely to be a byte blob whose first four bytes happen
// to be large.
const maxProbeVector = 4096

// Guess reports what a field most likely holds. See [Kind] for how much to
// trust it.
func (t Table) Guess(slot uint16) Kind {
	p := t.offset(slot)
	if p == 0 {
		return KindScalar
	}

	target := p + t.u32(p)
	if int(target)+4 > len(t.buf) {
		return KindScalar
	}
	n := t.u32(target)

	// A length prefix that fits the buffer: string, byte vector, or a vector of
	// offsets. All three look identical at this point.
	if n > 0 && int(target)+4+int(n) <= len(t.buf) {
		payload := t.buf[target+4 : target+4+n]
		if isPrintableRun(payload) {
			return KindString
		}
		if LooksRoaring(payload) {
			return KindRoaring
		}
		if n < maxProbeVector {
			checked, good := 0, 0
			for _, i := range probeIndices(n, maxProbeElems) {
				elem := target + 4 + i*4
				if int(elem)+4 > len(t.buf) {
					break
				}
				checked++
				if plausibleTable(t.buf, elem+t.u32(elem)) {
					good++
				}
			}
			// Require every probed element to resolve. One stray hit is easy;
			// four spread across the vector is not.
			if checked > 0 && good == checked {
				return KindVectorOfTables
			}
		}
		return KindBytes
	}

	if plausibleTable(t.buf, target) {
		return KindTable
	}
	return KindScalar
}

// plausibleTable reports whether pos looks like the start of a table: its
// soffset must resolve backwards to a vtable whose declared size is sane and
// inside the buffer.
func plausibleTable(buf []byte, pos uint32) bool {
	if int(pos)+4 > len(buf) {
		return false
	}
	soffset := int32(binary.LittleEndian.Uint32(buf[pos:]))
	vt := int32(pos) - soffset
	if vt < 0 || int(vt)+4 > len(buf) {
		return false
	}
	size := binary.LittleEndian.Uint16(buf[vt:])
	// A vtable is at least its own 4-byte header, and 512 bytes would be 254
	// fields: large enough for anything real, small enough to reject noise.
	return size >= 4 && size < 512 && int(vt)+int(size) <= len(buf)
}

// probeIndices picks up to max element indices spread evenly across a vector of
// n elements, always including the first and the last.
//
// Sampling the first few instead is what a straightforward loop does, and it is
// the worst possible choice. The head of a vector is where a coincidence is
// most likely to survive: small values near the start of a buffer land inside
// the buffer, and a struct vector whose first records hold small numbers can
// have every one of them resolve to something that passes plausibleTable. The
// same accident holding at the first, middle and last element at once is far
// less likely, and costs exactly the same number of probes.
func probeIndices(n, max uint32) []uint32 {
	if n == 0 || max == 0 {
		return nil
	}
	if n <= max {
		out := make([]uint32, n)
		for i := range out {
			out[i] = uint32(i)
		}
		return out
	}
	if max == 1 {
		return []uint32{0}
	}
	out := make([]uint32, max)
	for i := uint32(0); i < max; i++ {
		out[i] = i * (n - 1) / (max - 1)
	}
	return out
}

func isPrintableRun(b []byte) bool {
	if len(b) == 0 {
		return false
	}
	for _, c := range b {
		if c < 0x20 || c > 0x7e {
			return false
		}
	}
	return true
}

// LooksRoaring reports whether b starts with a RoaringBitmap serial cookie:
// 12346 for a plain bitmap, or 12347 in the low 16 bits when run containers are
// present.
//
// This only inspects the header. To actually decode one, hand the bytes to a
// Roaring library. This package deliberately has no dependencies.
func LooksRoaring(b []byte) bool {
	if len(b) < 8 {
		return false
	}
	cookie := binary.LittleEndian.Uint32(b)
	return cookie == 12346 || cookie&0xFFFF == 12347
}
