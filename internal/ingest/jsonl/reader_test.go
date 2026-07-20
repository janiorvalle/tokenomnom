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

func TestReadPositionedReportsCompleteRecordBounds(t *testing.T) {
	path := filepath.Join(t.TempDir(), "live.jsonl")
	if err := os.WriteFile(path, []byte("one\ntwo\npartial"), 0o644); err != nil {
		t.Fatal(err)
	}
	var got []Record
	position, err := ReadPositioned(path, Position{}, func(record Record) {
		record.Raw = append([]byte(nil), record.Raw...)
		got = append(got, record)
	})
	if err != nil {
		t.Fatal(err)
	}
	want := []Record{
		{Raw: []byte("one\n"), LineNumber: 1, StartOffset: 0, EndOffset: 4},
		{Raw: []byte("two\n"), LineNumber: 2, StartOffset: 4, EndOffset: 8},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("records = %#v, want %#v", got, want)
	}
	if position != (Position{ByteOffset: 8, LineNumber: 2}) {
		t.Fatalf("position = %#v, want offset 8 line 2", position)
	}
}

func TestReadPositionedResumesLineNumbers(t *testing.T) {
	path := filepath.Join(t.TempDir(), "live.jsonl")
	if err := os.WriteFile(path, []byte("one\ntwo\nthree\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	var got Record
	position, err := ReadPositioned(path, Position{ByteOffset: 8, LineNumber: 2}, func(record Record) { got = record })
	if err != nil {
		t.Fatal(err)
	}
	if string(got.Raw) != "three\n" || got.LineNumber != 3 || got.StartOffset != 8 || got.EndOffset != 14 {
		t.Fatalf("record = %#v", got)
	}
	if position != (Position{ByteOffset: 14, LineNumber: 3}) {
		t.Fatalf("position = %#v", position)
	}
}
