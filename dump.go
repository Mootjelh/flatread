package flatread

import (
	"fmt"
	"strings"
)

// DumpOptions controls [DumpWith]. The zero value is what [Dump] uses.
type DumpOptions struct {
	// MaxDepth limits how far nested tables are followed. 0 means the default
	// of 5, which is deep enough to see the shape of most payloads without
	// printing a megabyte.
	MaxDepth int

	// MaxVectorElems limits how many elements of a table vector are expanded.
	// 0 means the default of 3. The remainder is summarised as "... N more".
	MaxVectorElems int

	// Annotate, when set, is called for every byte vector and its result is
	// appended to that line. This is the hook for anything that needs a
	// dependency: counting bits in a RoaringBitmap, sniffing a compression
	// header, decoding a nested buffer. Return "" to add nothing.
	Annotate func(b []byte) string

	// StructSize, when set, is asked how wide the elements of a struct vector
	// are. Return 0 for any slot that does not hold one.
	//
	// It has to be asked because nothing in the buffer answers it. A vector of
	// structs and a vector of bytes are stored identically, a count followed by
	// inline data with no vtable and no offsets, so [Table.Guess] reports
	// [KindBytes] for both. Without this a vector of three 8-byte structs dumps
	// as "bytes 3" and prints three bytes of the first one, which looks like a
	// tiny byte vector rather than 24 bytes of records.
	//
	// The path is the slot numbers from the root down to and including the slot
	// in question: root slot 6 is {6}, and slot 6 of the table in root slot 4
	// is {4, 6}. Indices into a table vector are not part of it, because every
	// element of a vector shares one schema, so a path names a position in the
	// schema rather than in the buffer.
	StructSize func(path []uint16) int
}

func (o DumpOptions) maxDepth() int {
	if o.MaxDepth <= 0 {
		return 5
	}
	return o.MaxDepth
}

func (o DumpOptions) maxVectorElems() int {
	if o.MaxVectorElems <= 0 {
		return 3
	}
	return o.MaxVectorElems
}

// Dump walks a buffer from its root and describes every populated field,
// guessing each one's type from its bytes.
//
// This is the tool you reach for first when handed an unknown payload: it turns
// an opaque blob into a list of slot numbers with plausible types, which is
// what you need in order to start matching them against whatever documentation,
// generated code or client bundle you do have.
//
// A malformed buffer produces a description of the error rather than a panic.
func Dump(buf []byte) string {
	return DumpWith(buf, DumpOptions{})
}

// DumpWith is [Dump] with control over depth, breadth and annotation.
func DumpWith(buf []byte, opts DumpOptions) string {
	root, err := Root(buf)
	if err != nil {
		return err.Error()
	}
	var b strings.Builder
	fmt.Fprintf(&b, "root @0x%x (%d bytes)\n", root.pos, len(buf))
	dumpTable(&b, root, 1, map[uint32]bool{}, opts, nil)
	return b.String()
}

func dumpTable(b *strings.Builder, t Table, depth int, seen map[uint32]bool, opts DumpOptions, path []uint16) {
	// The seen set is not just an optimisation. Nothing stops a buffer, whether
	// by corruption or by design, from containing an offset cycle, and without
	// this the walk would not terminate on one.
	if depth > opts.maxDepth() || seen[t.pos] {
		return
	}
	seen[t.pos] = true
	ind := strings.Repeat("  ", depth)

	for _, slot := range t.Slots() {
		// A fresh slice per slot, deliberately not append(path, slot).
		//
		// Appending would share one backing array down the whole descent, so a
		// caller who keeps the path handed to StructSize would watch it change
		// as the walk moves on. It happens not to bite at shallow depth, since
		// this chain keeps length equal to capacity and append reallocates every
		// time, but that stops being true once the growth rounds capacity up,
		// which is around depth 4. A hazard that only appears in deep buffers
		// is not one to leave in.
		here := make([]uint16, len(path)+1)
		copy(here, path)
		here[len(path)] = slot

		// Asked before Guess is consulted, and it wins. The caller has a schema
		// and Guess has only bytes, so on the one question bytes cannot answer
		// the caller is right by definition.
		if size := structSizeAt(opts, here); size > 0 {
			dumpStructs(b, t, slot, size, ind, opts)
			dumpUnionHint(b, t, slot, ind)
			continue
		}

		switch kind := t.Guess(slot); kind {
		case KindString:
			fmt.Fprintf(b, "%sslot %-3d string   %q\n", ind, slot, t.String(slot))

		case KindRoaring:
			raw := t.Bytes(slot)
			fmt.Fprintf(b, "%sslot %-3d roaring  %d bytes%s\n", ind, slot, len(raw), annotate(opts, raw))

		case KindBytes:
			raw := t.Bytes(slot)
			fmt.Fprintf(b, "%sslot %-3d bytes    %d%s\n", ind, slot, len(raw), annotate(opts, raw))

		case KindVectorOfTables:
			n := t.VectorLen(slot)
			fmt.Fprintf(b, "%sslot %-3d vector   %d tables\n", ind, slot, n)
			limit := min(n, opts.maxVectorElems())
			for i := 0; i < limit; i++ {
				if sub, ok := t.TableAt(slot, i); ok {
					fmt.Fprintf(b, "%s  [%d]\n", ind, i)
					dumpTable(b, sub, depth+2, seen, opts, here)
				}
			}
			if n > limit {
				fmt.Fprintf(b, "%s  ... %d more\n", ind, n-limit)
			}

		case KindTable:
			fmt.Fprintf(b, "%sslot %-3d table\n", ind, slot)
			if sub, ok := t.Table(slot); ok {
				dumpTable(b, sub, depth+1, seen, opts, here)
			}

		default:
			// Show every width, because which one is right is precisely what is
			// unknown at this point.
			fmt.Fprintf(b, "%sslot %-3d scalar   u32=%d u16=%d u8=%d\n",
				ind, slot, t.Uint32(slot), t.Uint16(slot), t.Byte(slot))
		}

		dumpUnionHint(b, t, slot, ind)
	}
}

// dumpUnionHint says so when a slot looks like the tag half of a union. Read
// one at a time the two slots look unrelated: a small number here, a table two
// slots along. Say it, as a maybe.
func dumpUnionHint(b *strings.Builder, t Table, slot uint16, ind string) {
	shape, ok := t.UnionHint(slot)
	if !ok {
		return
	}
	if shape.Elements > 0 {
		fmt.Fprintf(b, "%s         maybe union tags for the %d tables in slot %d\n",
			ind, shape.Elements, shape.ValueSlot)
		return
	}
	fmt.Fprintf(b, "%s         maybe a union tag, value in slot %d\n",
		ind, shape.ValueSlot)
}

// structSizeAt asks the caller whether this slot holds a vector of structs.
//
// The path goes out as it is, which is safe because the walk gives every slot
// its own slice rather than sharing one array down the descent. See dumpTable.
func structSizeAt(opts DumpOptions, path []uint16) int {
	if opts.StructSize == nil {
		return 0
	}
	return opts.StructSize(path)
}

// dumpStructs renders a vector of fixed-size records.
//
// It reports a size that does not fit rather than falling back to the byte
// vector rendering. The caller stated a schema, and quietly showing something
// else would leave them reading the wrong answer to the question they asked.
func dumpStructs(b *strings.Builder, t Table, slot uint16, size int, ind string, opts DumpOptions) {
	n := t.StructVectorLen(slot, size)
	if n == 0 {
		fmt.Fprintf(b, "%sslot %-3d structs  no vector of %d-byte elements fits here\n", ind, slot, size)
		return
	}

	fmt.Fprintf(b, "%sslot %-3d structs  %d x %d bytes\n", ind, slot, n, size)
	limit := min(n, opts.maxVectorElems())
	for i := 0; i < limit; i++ {
		raw := t.StructAt(slot, i, size)
		fmt.Fprintf(b, "%s  [%d] %s%s\n", ind, i, hex(raw), asUint32s(raw))
	}
	if n > limit {
		fmt.Fprintf(b, "%s  ... %d more\n", ind, n-limit)
	}
}

func hex(raw []byte) string {
	var b strings.Builder
	for i, c := range raw {
		if i > 0 {
			b.WriteByte(' ')
		}
		fmt.Fprintf(&b, "%02x", c)
	}
	return b.String()
}

// asUint32s adds a little-endian uint32 reading when the element divides into
// them, on the same reasoning as the scalar line showing three widths at once:
// which one is right is exactly what is unknown, so show a plausible one beside
// the bytes rather than instead of them.
func asUint32s(raw []byte) string {
	if len(raw) == 0 || len(raw)%4 != 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString("   u32")
	for i := 0; i < len(raw); i += 4 {
		v := uint32(raw[i]) | uint32(raw[i+1])<<8 | uint32(raw[i+2])<<16 | uint32(raw[i+3])<<24
		fmt.Fprintf(&b, " %d", v)
	}
	return b.String()
}

func annotate(opts DumpOptions, raw []byte) string {
	if opts.Annotate == nil {
		return ""
	}
	if s := opts.Annotate(raw); s != "" {
		return "  " + s
	}
	return ""
}
