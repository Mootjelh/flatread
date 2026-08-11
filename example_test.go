package flatread

import "fmt"

// Dump is the first thing to run against an unfamiliar payload: it turns an
// opaque blob into a list of slot numbers with plausible types.
//
// Note slot 8, which really is a vector of uint32 {1, 2, 3} and is reported as
// three bytes. Nothing in a FlatBuffers buffer records a vector's element
// width, so that is the honest answer rather than a bug.
func ExampleDump() {
	fmt.Print(Dump(sample()))

	// Output:
	// root @0x40 (168 bytes)
	//   slot 4   scalar   u32=4242 u16=4242 u8=146
	//   slot 6   string   "hello"
	//   slot 8   bytes    3
	//   slot 10  table
	//     slot 4   scalar   u32=7 u16=7 u8=7
	//   slot 12  vector   2 tables
	//     [0]
	//       slot 4   scalar   u32=11 u16=11 u8=11
	//     [1]
	//       slot 4   scalar   u32=22 u16=22 u8=22
}

// Once Dump has told you which slots exist, read them with the typed
// accessors. Slot numbers are the schema you did not get.
func ExampleTable() {
	root, err := Root(sample())
	if err != nil {
		panic(err)
	}

	fmt.Println("slots: ", root.Slots())
	fmt.Println("string:", root.String(6))
	fmt.Println("vector:", root.Int32Vector(8))

	if nested, ok := root.Table(10); ok {
		fmt.Println("nested:", nested.Uint32(4))
	}
	for i := 0; i < root.VectorLen(12); i++ {
		if sub, ok := root.TableAt(12, i); ok {
			fmt.Printf("elem %d: %d\n", i, sub.Uint32(4))
		}
	}

	// Output:
	// slots:  [4 6 8 10 12]
	// string: hello
	// vector: [1 2 3]
	// nested: 7
	// elem 0: 11
	// elem 1: 22
}
