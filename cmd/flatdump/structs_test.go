package main

import "testing"

func TestStructFlagParses(t *testing.T) {
	cases := []struct {
		in       string
		wantKey  string
		wantSize int
	}{
		{"6:8", "6", 8},
		{"4.6:12", "4.6", 12},
		{"0.1.2.3:4", "0.1.2.3", 4},
		{"65535:1", "65535", 1},
	}

	for _, c := range cases {
		f := structFlag{}
		if err := f.Set(c.in); err != nil {
			t.Errorf("Set(%q): %v", c.in, err)
			continue
		}
		if got := f[c.wantKey]; got != c.wantSize {
			t.Errorf("Set(%q) put %d at %q, want %d", c.in, got, c.wantKey, c.wantSize)
		}
	}
}

func TestStructFlagRejects(t *testing.T) {
	for _, in := range []string{
		"6",       // no size
		"6:0",     // a zero-byte record is not a record
		"6:-4",    // nor a negative one
		"6:x",     // not a number
		":8",      // no path
		"a.b:8",   // not slot numbers
		"4..6:8",  // empty component
		"65536:8", // past uint16
		"6:8:10",  // Cut takes the first colon, so the size reads as "8:10"
		"",        // nothing at all
	} {
		f := structFlag{}
		if err := f.Set(in); err == nil {
			t.Errorf("Set(%q) was accepted, want an error", in)
		}
	}
}

// The flag names a path, not a slot number, and this is why. The dump walks
// every nested table, so a bare slot number would claim that slot in all of
// them and render ordinary byte vectors as records.
func TestStructFlagTargetsOnlyTheNamedPath(t *testing.T) {
	f := structFlag{}
	if err := f.Set("4.6:8"); err != nil {
		t.Fatal(err)
	}

	if got := f.sizeAt([]uint16{4, 6}); got != 8 {
		t.Errorf("sizeAt({4, 6}) = %d, want 8", got)
	}
	for _, path := range [][]uint16{
		{6},       // the same slot at the root
		{4},       // the table on the way there
		{5, 6},    // the same slot under a different parent
		{4, 6, 6}, // deeper than named
		nil,
	} {
		if got := f.sizeAt(path); got != 0 {
			t.Errorf("sizeAt(%v) = %d, want 0", path, got)
		}
	}
}

func TestStructFlagTakesSeveral(t *testing.T) {
	f := structFlag{}
	for _, in := range []string{"6:8", "4.6:12"} {
		if err := f.Set(in); err != nil {
			t.Fatal(err)
		}
	}

	if got := f.sizeAt([]uint16{6}); got != 8 {
		t.Errorf("sizeAt({6}) = %d, want 8", got)
	}
	if got := f.sizeAt([]uint16{4, 6}); got != 12 {
		t.Errorf("sizeAt({4, 6}) = %d, want 12", got)
	}
}
