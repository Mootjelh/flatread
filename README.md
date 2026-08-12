# flatread

[![CI](https://github.com/Mootjelh/flatread/actions/workflows/ci.yml/badge.svg)](https://github.com/Mootjelh/flatread/actions/workflows/ci.yml)
[![Go Reference](https://pkg.go.dev/badge/github.com/Mootjelh/flatread.svg)](https://pkg.go.dev/github.com/Mootjelh/flatread)

Read FlatBuffers buffers when you don't have the schema.

No dependencies. Every accessor is bounds-checked, so malformed input returns a zero value instead of panicking.

## Why

The normal FlatBuffers workflow compiles a `.fbs` schema into generated accessors, and the field names live in that generated code. If you're handed a binary payload without its schema there's nothing to generate from, and the official library can't help, because it needs the types you're trying to work out.

That comes up more often than the docs suggest:

* reverse engineering a protocol from a capture
* inspecting a payload from a service that doesn't publish its schema
* triaging a file that decodes on one machine but not another
* checking what an SDK actually sends, rather than what its docs claim

flatread reads a buffer positionally. Fields are addressed by vtable slot number instead of by name. That's enough to walk an unknown buffer, and enough to write a real decoder once you've worked out which slot means what.

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

It reads stdin when given no file, so it drops into a pipeline:

```bash
curl -s https://example.com/payload | flatdump -depth 8
```

With `-json` the same walk comes out machine-readable, which is what you want once you are grepping across a directory of captures:

```bash
flatdump -json payload.bin | jq '.fields[] | select(.kind == "string")'
```

## Buffers that are not plain

Two things are worth checking before you conclude a payload is empty.

**Size-prefixed buffers.** gRPC and any format that stores several messages back to back put a uint32 byte count in front of the buffer. `flatdump` detects that and says so; in code it is `RootSizePrefixed`, with `IsSizePrefixed` for the guess.

This matters because feeding such a buffer to plain `Root` does not fail loudly. It reads the size as the root offset, lands somewhere arbitrary, and usually reports a table with no fields at all:

```
$ flatdump -prefix no sized.bin
root @0xa8 (172 bytes)
```

No error, no fields, and identical to a buffer that genuinely has nothing in it.

**File identifiers.** A schema can attach four bytes of identifier directly after the root offset, and when it is there it is usually the fastest way to find out what you are holding, because it is picked to be human-readable:

```
$ flatdump payload.bin
file identifier "FLTR"
root @0x40 (168 bytes)
...
```

Nothing in the buffer records whether that field is present, so `FileIdentifier` returns the bytes plus whether they look like an identifier at all. A false there means "these are not plausible", not "this schema has none".

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

Scalars (`Byte`, `Bool`, `Int8` through `Int64`, `Uint16` through `Uint64`, `Float32`, `Float64`), strings, byte vectors, numeric and string vectors, nested tables and vectors of tables. `Has` distinguishes an absent field from one that's present and zero.

Entry points: `Root` for an ordinary buffer, `RootSizePrefixed` for one carrying a length, `TableAtOffset` when you already know where a table starts.

## How a buffer is laid out

Worth having to hand while reading a dump. Everything is little-endian.

```
buf[0:4]                 uoffset to the root table

table[-soffset]          the table's vtable. soffset is a SIGNED int32
                         stored at the table's own position, so the
                         vtable normally sits before the table
vtable[0:2]              vtable size in bytes
vtable[2:4]              inline table size in bytes
vtable[4+2i : 6+2i]      byte offset of field i inside the table,
                         0 meaning absent
```

Field `i` lives at slot number `4+2i`, so the first field is slot 4, the second slot 6, the third slot 8. Slot numbers are what you pass to every accessor. Without a schema they're the only stable handle you have.

Offsets to strings, vectors and nested tables are uoffsets: unsigned, and relative to the position of the offset itself.

## Limitations

flatread recovers structure, not types. `Guess` works from a length prefix that fits, a run of printable bytes, a known magic number, or an offset that resolves to a readable vtable. That's genuinely all the information the buffer holds.

The clearest consequence, which has a test pinning it so it doesn't get "fixed" later: a vector of `uint32{1, 2, 3}` and the twelve bytes that encode it are the same twelve bytes, so `Guess` reports `bytes`. Nothing in a FlatBuffers buffer records a vector's element width.

So use `Guess` to find the slots you care about, confirm each one against real data, then read them with the typed accessors. Don't build a decoder on it.

flatread also doesn't write buffers. Use the [official library](https://github.com/google/flatbuffers) for that. Once you know the schema you should be generating code from it anyway.

## Safety

Every accessor is bounds-checked and returns a zero value rather than panicking. This package is by definition pointed at bytes nobody has validated, so malformed input is the ordinary case here, not an exceptional one.

That property is enforced rather than claimed:

```bash
go test -fuzz=FuzzReader
```

The fuzz target walks every accessor over arbitrary input, including offset cycles: a buffer whose field points back at its own table, which an encoder would never emit but a corrupt or hostile file can. CI runs a 60-second pass on every push.

The trade-off is that a zero return is ambiguous. It means either "the field holds zero" or "there is no such field". Use `Has` when the difference matters.

## Roaring bitmaps

Byte vectors carrying a [RoaringBitmap](https://roaringbitmap.org/) serial cookie are recognised and labelled, since Roaring turns up often in search and analytics payloads. Decoding one needs a dependency this package doesn't take, so there's a hook for it:

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
