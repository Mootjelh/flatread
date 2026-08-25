// Command flatdump prints the structure of a FlatBuffers buffer you have no
// schema for.
//
//	flatdump payload.bin
//	curl -s https://example.com/payload | flatdump
//	flatdump -depth 8 -elems 10 payload.bin
//	flatdump -json payload.bin | jq '.fields[] | select(.kind == "string")'
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"

	"github.com/Mootjelh/flatread"
)

func main() {
	depth := flag.Int("depth", 5, "how far to follow nested tables")
	elems := flag.Int("elems", 3, "how many elements of a table vector to expand")
	asJSON := flag.Bool("json", false, "emit JSON instead of the indented listing")
	prefix := flag.String("prefix", "auto", "size prefix handling: auto, yes or no")
	other := flag.String("diff", "", "compare the given file against `file`, and list the slots that differ")
	flag.Usage = func() {
		fmt.Fprintf(os.Stderr, `usage: flatdump [flags] [file]

Reads stdin when no file is given.

With -diff, exits 0 when the two buffers agree, 1 when they differ, and 2 on
an error, the same way diff(1) does.

flags:
`)
		flag.PrintDefaults()
	}
	flag.Parse()

	buf, err := read(flag.Arg(0))
	if err != nil {
		fail(err)
	}

	sized, err := sizePrefixed(*prefix, buf)
	if err != nil {
		fail(err)
	}

	root, err := open(buf, sized)
	if err != nil {
		fail(err)
	}

	if *other != "" {
		runDiff(*other, *prefix, root)
		return
	}

	id, hasID := flatread.FileIdentifier(payload(buf, sized))

	if *asJSON {
		emitJSON(buf, root, sized, id, hasID, *depth, *elems)
		return
	}

	if sized {
		fmt.Printf("size-prefixed, %d bytes total\n", len(buf))
	}
	if hasID {
		fmt.Printf("file identifier %q\n", id)
	}
	fmt.Print(flatread.DumpWith(payload(buf, sized), flatread.DumpOptions{
		MaxDepth:       *depth,
		MaxVectorElems: *elems,
		Annotate:       annotate,
	}))
}

// runDiff lists the slots that differ, taking the -diff file as the left side
// so that `flatdump -diff old.bin new.bin` reads old to new.
func runDiff(otherPath, prefixMode string, b flatread.Table) {
	obuf, err := read(otherPath)
	if err != nil {
		fail(err)
	}
	osized, err := sizePrefixed(prefixMode, obuf)
	if err != nil {
		fail(err)
	}
	a, err := open(obuf, osized)
	if err != nil {
		fail(err)
	}

	changes := flatread.Diff(a, b)
	if len(changes) == 0 {
		return
	}
	for _, c := range changes {
		fmt.Println(c)
	}
	os.Exit(1)
}

// fail exits 2, leaving 1 to mean "the buffers differ" under -diff.
func fail(err error) {
	fmt.Fprintln(os.Stderr, "flatdump:", err)
	os.Exit(2)
}

func read(path string) ([]byte, error) {
	if path == "" || path == "-" {
		return io.ReadAll(os.Stdin)
	}
	return os.ReadFile(path)
}

// sizePrefixed resolves the -prefix flag into a decision. "auto" guesses, which
// is what you want on a payload of unknown provenance and not what you want
// once you know the format, hence the override.
func sizePrefixed(mode string, buf []byte) (bool, error) {
	switch mode {
	case "auto":
		return flatread.IsSizePrefixed(buf), nil
	case "yes":
		return true, nil
	case "no":
		return false, nil
	default:
		return false, fmt.Errorf("-prefix must be auto, yes or no (got %q)", mode)
	}
}

func open(buf []byte, sized bool) (flatread.Table, error) {
	if sized {
		return flatread.RootSizePrefixed(buf)
	}
	return flatread.Root(buf)
}

// payload is the buffer with any size prefix removed, which is what the root
// offset and the file identifier are relative to.
func payload(buf []byte, sized bool) []byte {
	if sized {
		return buf[4:]
	}
	return buf
}

// annotate adds a short preview of any byte vector, which is usually enough to
// recognise a nested buffer, a compressed blob or a run of ids by eye.
func annotate(b []byte) string {
	const preview = 12
	n := len(b)
	if n > preview {
		b = b[:preview]
	}
	out := ""
	for _, c := range b {
		out += fmt.Sprintf("%02x ", c)
	}
	if n > preview {
		out += "..."
	}
	return out
}

// --- JSON --------------------------------------------------------------------

type doc struct {
	Bytes          int    `json:"bytes"`
	SizePrefixed   bool   `json:"size_prefixed"`
	FileIdentifier string `json:"file_identifier,omitempty"`
	Root           uint32 `json:"root"`
	Fields         []node `json:"fields"`
}

type node struct {
	Slot     uint16 `json:"slot"`
	Kind     string `json:"kind"`
	String   string `json:"string,omitempty"`
	Uint32   uint32 `json:"uint32,omitempty"`
	Bytes    int    `json:"bytes,omitempty"`
	Length   int    `json:"length,omitempty"`
	Children []node `json:"children,omitempty"`

	// MaybeUnion is set when this slot looks like the tag half of a union. It
	// is a guess about shape, not something the buffer records, hence the name.
	MaybeUnion *unionNote `json:"maybe_union,omitempty"`
}

type unionNote struct {
	ValueSlot uint16 `json:"value_slot"`
	Elements  int    `json:"elements,omitempty"`
}

// emitJSON walks the buffer through the exported API only. That is deliberate:
// if the CLI needed anything unexported, the library would be missing it.
func emitJSON(buf []byte, root flatread.Table, sized bool, id string, hasID bool, depth, elems int) {
	d := doc{
		Bytes:        len(buf),
		SizePrefixed: sized,
		Root:         root.Pos(),
		Fields:       walk(root, depth, elems, map[uint32]bool{}),
	}
	if hasID {
		d.FileIdentifier = id
	}

	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	if err := enc.Encode(d); err != nil {
		fail(err)
	}
}

func walk(t flatread.Table, depth, elems int, seen map[uint32]bool) []node {
	if depth <= 0 || seen[t.Pos()] {
		return nil
	}
	seen[t.Pos()] = true

	var out []node
	for _, slot := range t.Slots() {
		kind := t.Guess(slot)
		n := node{Slot: slot, Kind: kind.String()}

		switch kind {
		case flatread.KindString:
			n.String = t.String(slot)
		case flatread.KindBytes, flatread.KindRoaring:
			n.Bytes = len(t.Bytes(slot))
		case flatread.KindTable:
			if sub, ok := t.Table(slot); ok {
				n.Children = walk(sub, depth-1, elems, seen)
			}
		case flatread.KindVectorOfTables:
			count := t.VectorLen(slot)
			n.Length = count
			if count > elems {
				count = elems
			}
			for i := 0; i < count; i++ {
				if sub, ok := t.TableAt(slot, i); ok {
					n.Children = append(n.Children, node{
						Slot:     uint16(i),
						Kind:     "element",
						Children: walk(sub, depth-1, elems, seen),
					})
				}
			}
		default:
			n.Uint32 = t.Uint32(slot)
		}

		if shape, ok := t.UnionHint(slot); ok {
			n.MaybeUnion = &unionNote{ValueSlot: shape.ValueSlot, Elements: shape.Elements}
		}

		out = append(out, n)
	}
	return out
}
