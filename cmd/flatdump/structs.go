package main

import (
	"fmt"
	"strconv"
	"strings"
)

// structFlag collects -struct values. Nothing in a buffer says whether a vector
// holds structs or bytes, so this is how the schema knowledge gets in.
//
// The syntax is path:size, where path is one or more slot numbers separated by
// dots:
//
//	-struct 6:8      slot 6 of the root holds 8-byte structs
//	-struct 4.6:12   slot 6 of the table in root slot 4 holds 12-byte ones
//
// A plain slot number is not enough on its own. The dump walks every nested
// table, so "slot 6" would also claim slot 6 of every other table in the
// buffer, and render a perfectly ordinary byte vector as records.
type structFlag map[string]int

func (f structFlag) String() string {
	if len(f) == 0 {
		return ""
	}
	out := make([]string, 0, len(f))
	for path, size := range f {
		out = append(out, fmt.Sprintf("%s:%d", path, size))
	}
	return strings.Join(out, ",")
}

func (f structFlag) Set(v string) error {
	path, sizeStr, ok := strings.Cut(v, ":")
	if !ok {
		return fmt.Errorf("want path:size, for example 6:8 (got %q)", v)
	}

	size, err := strconv.Atoi(sizeStr)
	if err != nil || size <= 0 {
		return fmt.Errorf("element size must be a positive number of bytes (got %q)", sizeStr)
	}

	if path == "" {
		return fmt.Errorf("want at least one slot number before the colon (got %q)", v)
	}
	for _, part := range strings.Split(path, ".") {
		n, err := strconv.Atoi(part)
		if err != nil || n < 0 || n > 65535 {
			return fmt.Errorf("slot numbers must be between 0 and 65535 (got %q)", part)
		}
	}

	f[key(parse(path))] = size
	return nil
}

// parse turns "4.6" into {4, 6}. Set has already checked every part, so this
// cannot fail by the time it runs.
func parse(path string) []uint16 {
	parts := strings.Split(path, ".")
	out := make([]uint16, 0, len(parts))
	for _, p := range parts {
		n, _ := strconv.Atoi(p)
		out = append(out, uint16(n))
	}
	return out
}

// key renders a path as the map key. The flag and the lookup both go through
// this, so the two cannot drift into different spellings of the same path.
func key(path []uint16) string {
	parts := make([]string, len(path))
	for i, s := range path {
		parts[i] = strconv.Itoa(int(s))
	}
	return strings.Join(parts, ".")
}

// sizeAt is the DumpOptions.StructSize hook: 0 for every slot not named.
func (f structFlag) sizeAt(path []uint16) int {
	return f[key(path)]
}
