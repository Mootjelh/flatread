package flatread

import (
	"fmt"
	"strconv"
)

// Change is one difference between two buffers.
type Change struct {
	// Path is the slot path from the root, using dots for nested tables and
	// brackets for elements of a table vector: "4", "10.4", "12[0].4".
	Path string

	// A and B are the rendered values on each side. An empty string means the
	// slot is not present on that side, which [Change.String] shows as absent.
	A, B string
}

func (c Change) String() string {
	a, b := c.A, c.B
	if a == "" {
		a = "(absent)"
	}
	if b == "" {
		b = "(absent)"
	}
	return fmt.Sprintf("%-14s %s -> %s", c.Path, a, b)
}

// diffDepth and diffElems match the dumper's defaults, so a diff covers the
// same ground a dump shows.
const (
	diffDepth = 5
	diffElems = 3
)

// Diff reports the slots that differ between two tables, walking nested tables
// and table vectors.
//
// This is the workflow the tool exists for: capture the same request twice, or
// the same event before and after something changed, and ask which slots moved.
// It is far faster than reading two dumps side by side, and it does not need a
// schema, because a slot path is a stable handle whether or not you know what
// the slot means.
//
// Values are compared as rendered text rather than as bytes. That is deliberate:
// two slots holding the same number are equal even if one buffer padded
// differently, and a change of kind (a string becoming a table) shows up as a
// change rather than as an error.
//
// A nil result means the two tables agree everywhere Diff looked. Depth is
// capped, so it means "no difference within reach", not "the buffers are
// identical".
func Diff(a, b Table) []Change {
	return diffTables("", a, b, 0, map[[2]uint32]bool{})
}

func diffTables(prefix string, a, b Table, depth int, seen map[[2]uint32]bool) []Change {
	if depth > diffDepth {
		return nil
	}
	// Both sides can contain a cycle, so the visited key is the pair. Guarding
	// on one side alone would still loop when only the other repeats.
	key := [2]uint32{a.Pos(), b.Pos()}
	if seen[key] {
		return nil
	}
	seen[key] = true

	var out []Change
	for _, slot := range unionOfSlots(a, b) {
		path := prefix + strconv.Itoa(int(slot))

		switch {
		case !a.Has(slot):
			out = append(out, Change{Path: path, B: render(b, slot)})
			continue
		case !b.Has(slot):
			out = append(out, Change{Path: path, A: render(a, slot)})
			continue
		}

		ka, kb := a.Guess(slot), b.Guess(slot)
		if ka != kb {
			out = append(out, Change{Path: path, A: render(a, slot), B: render(b, slot)})
			continue
		}

		switch ka {
		case KindTable:
			sa, oka := a.Table(slot)
			sb, okb := b.Table(slot)
			if oka && okb {
				out = append(out, diffTables(path+".", sa, sb, depth+1, seen)...)
				continue
			}
			// One side did not resolve. Report it rather than silently agreeing.
			out = append(out, Change{Path: path, A: render(a, slot), B: render(b, slot)})

		case KindVectorOfTables:
			na, nb := a.VectorLen(slot), b.VectorLen(slot)
			if na != nb {
				out = append(out, Change{
					Path: path,
					A:    fmt.Sprintf("%d tables", na),
					B:    fmt.Sprintf("%d tables", nb),
				})
				continue
			}
			for i := 0; i < na && i < diffElems; i++ {
				sa, oka := a.TableAt(slot, i)
				sb, okb := b.TableAt(slot, i)
				if !oka || !okb {
					continue
				}
				elem := fmt.Sprintf("%s[%d].", path, i)
				out = append(out, diffTables(elem, sa, sb, depth+1, seen)...)
			}

		default:
			if va, vb := render(a, slot), render(b, slot); va != vb {
				out = append(out, Change{Path: path, A: va, B: vb})
			}
		}
	}
	return out
}

// unionOfSlots returns every slot populated on either side, ascending, so a
// field that exists in only one buffer is still compared.
func unionOfSlots(a, b Table) []uint16 {
	seen := map[uint16]bool{}
	var out []uint16
	for _, s := range a.Slots() {
		if !seen[s] {
			seen[s] = true
			out = append(out, s)
		}
	}
	for _, s := range b.Slots() {
		if !seen[s] {
			seen[s] = true
			out = append(out, s)
		}
	}
	// Slots() is already ascending on each side; merge by insertion since the
	// counts here are small.
	for i := 1; i < len(out); i++ {
		for j := i; j > 0 && out[j] < out[j-1]; j-- {
			out[j], out[j-1] = out[j-1], out[j]
		}
	}
	return out
}

// render turns a slot into comparable, printable text.
func render(t Table, slot uint16) string {
	if !t.Has(slot) {
		return ""
	}
	switch t.Guess(slot) {
	case KindString:
		return strconv.Quote(t.String(slot))
	case KindRoaring:
		return fmt.Sprintf("roaring %d bytes", len(t.Bytes(slot)))
	case KindBytes:
		raw := t.Bytes(slot)
		const preview = 8
		if len(raw) > preview {
			return fmt.Sprintf("%d bytes %x...", len(raw), raw[:preview])
		}
		return fmt.Sprintf("%d bytes %x", len(raw), raw)
	case KindVectorOfTables:
		return fmt.Sprintf("%d tables", t.VectorLen(slot))
	case KindTable:
		return "table"
	default:
		return strconv.FormatUint(uint64(t.Uint32(slot)), 10)
	}
}
