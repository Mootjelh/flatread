package flatread

import "testing"

// FuzzReader asserts the central safety property of this package: no input,
// however malformed, makes an accessor panic.
//
// That matters more here than in most libraries. A schema-less reader is by
// definition pointed at bytes nobody has validated: a capture, a truncated
// file, a payload from a service that changed shape overnight. So malformed
// input is the ordinary case. Every accessor is therefore bounds-checked and
// returns a zero value, and this is what keeps that true.
//
// Run the seed corpus with `go test`, or search for new inputs with:
//
//	go test -fuzz=FuzzReader
func FuzzReader(f *testing.F) {
	f.Add(sample())
	f.Add(selfReferential())
	f.Add([]byte{})
	f.Add([]byte{0xff, 0xff, 0xff, 0xff, 0, 0, 0, 0})
	f.Add(make([]byte, 64))

	f.Add(sizePrefixed(sample()))

	f.Fuzz(func(t *testing.T, data []byte) {
		// The buffer-level helpers take raw bytes and must survive anything.
		_ = IsSizePrefixed(data)
		_, _ = FileIdentifier(data)
		if sized, err := RootSizePrefixed(data); err == nil {
			walk(sized, 0)
		}

		root, err := Root(data)
		if err != nil {
			return
		}

		walk(root, 0)

		// Dump exercises the same accessors through a different path, including
		// the recursion guard.
		_ = Dump(data)

		// StructSize is answered by a caller holding a schema, so it can name a
		// size for a slot that holds nothing of the sort, or one far larger
		// than the buffer. The struct rendering has to survive all of it.
		for _, size := range []int{1, 3, 8, 1 << 20} {
			_ = DumpWith(data, DumpOptions{
				StructSize: func([]uint16) int { return size },
			})
		}

		// Only is a path a caller typed, so it can name slots that are not
		// there, go deeper than the buffer does, or be longer than any table.
		for _, only := range [][]uint16{
			{0}, {4}, {65535}, {4, 6}, {4, 6, 8, 10, 12, 14},
		} {
			_ = DumpWith(data, DumpOptions{Only: only})
			_ = OnPath(only, nil)
			_ = OnPath(nil, only)
			_ = OnPath(only, only)
		}

		// Diff walks two buffers at once, so its cycle guard is keyed on the
		// pair. Compare arbitrary input against a known-good buffer both ways
		// round, and against itself.
		if good, err := Root(sample()); err == nil {
			_ = Diff(root, good)
			_ = Diff(good, root)
		}
		_ = Diff(root, root)
	})
}

func walk(t Table, depth int) {
	if depth > 4 {
		return
	}
	for _, slot := range t.Slots() {
		_ = t.Has(slot)
		_ = t.Guess(slot)

		_ = t.Byte(slot)
		_ = t.Bool(slot)
		_ = t.Int8(slot)
		_ = t.Uint16(slot)
		_ = t.Int16(slot)
		_ = t.Uint32(slot)
		_ = t.Int32(slot)
		_ = t.Uint64(slot)
		_ = t.Int64(slot)
		_ = t.Float32(slot)
		_ = t.Float64(slot)

		_ = t.String(slot)
		_ = t.Bytes(slot)

		_ = t.VectorLen(slot)
		_ = t.Int32Vector(slot)
		_ = t.Uint32Vector(slot)
		_ = t.Int64Vector(slot)
		_ = t.StringVector(slot)
		_ = t.Float32Vector(slot)
		_ = t.Float64Vector(slot)

		_ = t.UnionType(slot)
		_, _, _ = t.Union(slot)
		_ = t.UnionVectorLen(slot)
		_, _ = t.UnionHint(slot)
		for _, i := range []int{-1, 0, 1, 1 << 20} {
			_, _, _ = t.UnionAt(slot, i)
		}

		// Struct sizes come from a schema, so a caller can pass anything at
		// all, including sizes that do not divide the vector.
		for _, size := range []int{-1, 0, 1, 3, 8, 4096} {
			_ = t.StructVectorLen(slot, size)
			for _, i := range []int{-1, 0, 1, 1 << 20} {
				_ = t.StructAt(slot, i, size)
			}
		}

		// Index past the end as well as inside it: the bounds check on element
		// access is easy to get right for element 0 and wrong for the rest.
		for _, i := range []int{-1, 0, 1, t.VectorLen(slot)} {
			_ = t.StringAt(slot, i)
			_ = t.BytesAt(slot, i)
			if sub, ok := t.TableAt(slot, i); ok {
				walk(sub, depth+1)
			}
		}

		if sub, ok := t.Table(slot); ok {
			walk(sub, depth+1)
		}
	}
}
