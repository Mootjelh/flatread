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
	dumpTable(&b, root, 1, map[uint32]bool{}, opts)
	return b.String()
}

func dumpTable(b *strings.Builder, t Table, depth int, seen map[uint32]bool, opts DumpOptions) {
	// The seen set is not just an optimisation. Nothing stops a buffer — whether
	// by corruption or by design — from containing an offset cycle, and without
	// this the walk would not terminate on one.
	if depth > opts.maxDepth() || seen[t.pos] {
		return
	}
	seen[t.pos] = true
	ind := strings.Repeat("  ", depth)

	for _, slot := range t.Slots() {
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
					dumpTable(b, sub, depth+2, seen, opts)
				}
			}
			if n > limit {
				fmt.Fprintf(b, "%s  ... %d more\n", ind, n-limit)
			}

		case KindTable:
			fmt.Fprintf(b, "%sslot %-3d table\n", ind, slot)
			if sub, ok := t.Table(slot); ok {
				dumpTable(b, sub, depth+1, seen, opts)
			}

		default:
			// Show every width, because which one is right is precisely what is
			// unknown at this point.
			fmt.Fprintf(b, "%sslot %-3d scalar   u32=%d u16=%d u8=%d\n",
				ind, slot, t.Uint32(slot), t.Uint16(slot), t.Byte(slot))
		}
	}
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
