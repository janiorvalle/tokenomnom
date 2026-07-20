package jsonl

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func TestReadCompleteLeavesPartialTail(t *testing.T) {
	path := filepath.Join(t.TempDir(), "live.jsonl")
	if err := os.WriteFile(path, []byte("one\ntwo\npartial"), 0o644); err != nil {
		t.Fatal(err)
	}
	var got []string
	offset, err := ReadComplete(path, 0, func(line []byte) { got = append(got, string(line)) })
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, []string{"one\n", "two\n"}) || offset != 8 {
		t.Fatalf("got lines %q offset %d, want two complete lines and offset 8", got, offset)
	}
}
