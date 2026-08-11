# flatread

[![Go Reference](https://pkg.go.dev/badge/github.com/Mootjelh/flatread.svg)](https://pkg.go.dev/github.com/Mootjelh/flatread)
[![Go Report Card](https://goreportcard.com/badge/github.com/Mootjelh/flatread)](https://goreportcard.com/report/github.com/Mootjelh/flatread)

Read FlatBuffers buffers you have **no schema** for.

No dependencies. Never panics, on any input.

## Why

The normal FlatBuffers workflow compiles a `.fbs` schema into generated accessors, and the field names live in that generated code. When you are handed a binary payload *without* its schema, there is nothing to generate from and the official library cannot help you — it needs the types you are trying to work out.

That happens more often than the docs suggest:

- reverse engineering a protocol from a capture
- inspecting a payload from a service whose schema is not published
- triaging a file that decodes on one machine and not another
- checking what an SDK actually sends, rather than what its docs claim

`flatread` reads a buffer **positionally**: fields are addressed by vtable slot number instead of by name. That is enough to walk an unknown buffer, and enough to write a real decoder once you have worked out which slot means what.

## Install

```bash
go get github.com/Mootjelh/flatread
```

```bash
go install github.com/Mootjelh/flatread/cmd/flatdump@latest
```

## The CLI

Point `flatdump` at a payload and it describes every populated field, guessing each one's type from its bytes:

```
$ flatdump payload.bin
root @0x40 (168 bytes)
  slot 4   scalar   u32=4242 u16=4242 u8=146
  slot 6   string   "hello"
  slot 8   bytes    3
  slot 10  table
    slot 4   scalar   u32=7 u16=7 u8=7
  slot 12  vector   2 tables
    [0]
      slot 4   scalar   u32=11 u16=11 u8=11
    [1]
      slot 4   scalar   u32=22 u16=22 u8=22
```

It reads stdin when given no file, so it drops straight into a pipeline:

```bash
curl -s https://example.com/payload | flatdump -depth 8
```

## The library

```go
root, err := flatread.Root(buf)
if err != nil {
    return err
}

for _, slot := range root.Slots() {
    fmt.Println(slot, root.Guess(slot))
}

name := root.String(6)
ids  := root.Int32Vector(8)

if pricing, ok := root.Table(10); ok {
    total := pricing.Uint32(4)
}

for i := 0; i < root.VectorLen(12); i++ {
    if offer, ok := root.TableAt(12, i); ok {
        _ = offer.String(4)
    }
}
```

Scalars (`Byte`, `Bool`, `Int8`…`Int64`, `Uint16`…`Uint64`, `Float32`, `Float64`), strings, byte vectors, numeric vectors, nested tables and vectors of tables. `Has` distinguishes an absent field from one that is present and zero.

## How a buffer is laid out

Worth having to hand while reading a dump. Everything is little-endian.

```
buf[0:4]                 uoffset to the root table

table[-soffset]          the table's vtable. soffset is a SIGNED int32
                         stored at the table's own position, so the
                         vtable normally sits BEFORE the table
vtable[0:2]              vtable size in bytes
vtable[2:4]              inline table size in bytes
vtable[4+2i : 6+2i]      byte offset of field i inside the table,
                         0 meaning absent
```

So **field _i_ lives at slot number `4+2i`**: the first field is slot 4, the second slot 6, the third slot 8. Slot numbers are what you pass to every accessor — they are the only stable handle you have without a schema.

Offsets to strings, vectors and nested tables are `uoffset`s: unsigned, and relative to the position of the offset itself.

## What it deliberately does not do

**It cannot recover types, only structure.** `Guess` reads a length prefix that fits, a run of printable bytes, a known magic number, an offset that resolves to a readable vtable. That is genuinely all the information in the buffer.

The sharpest consequence, pinned by a test so it does not get "fixed":

> A vector of `uint32{1, 2, 3}` and the twelve bytes that encode it **are the same twelve bytes.** `Guess` reports `bytes`. Nothing records a vector's element width.

Use `Guess` to find the slots you care about, confirm each against real data, then read them with the typed accessors. Do not build a decoder on it.

It also does not **write** buffers. Use the [official library](https://github.com/google/flatbuffers) for that — once you know the schema, you should be generating code from it anyway.

## Safety

Every accessor is bounds-checked and returns a zero value rather than panicking, because this package is by definition pointed at bytes nobody has validated. Malformed input is the ordinary case here, not an exceptional one.

That property is enforced, not asserted:

```bash
go test -fuzz=FuzzReader
```

The fuzz target walks every accessor over arbitrary input, including offset cycles — a buffer whose field points back at its own table, which an encoder would never emit but a corrupt or hostile file certainly can.

The trade-off is that a zero return is ambiguous: it means either "the field holds zero" or "there is no such field". Use `Has` when the difference matters.

## Roaring bitmaps

Byte vectors carrying a [RoaringBitmap](https://roaringbitmap.org/) serial cookie are recognised and labelled, since Roaring turns up often in search and analytics payloads. Decoding one needs a dependency this package does not take — use the `Annotate` hook:

```go
flatread.DumpWith(buf, flatread.DumpOptions{
    Annotate: func(b []byte) string {
        if !flatread.LooksRoaring(b) {
            return ""
        }
        bm := roaring.New()
        if _, err := bm.ReadFrom(bytes.NewReader(b)); err != nil {
            return "roaring: " + err.Error()
        }
        return fmt.Sprintf("%d bits set", bm.GetCardinality())
    },
})
```

## License

MIT
