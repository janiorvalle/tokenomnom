// Package jsonl provides checkpoint-aware reading of complete JSONL records.
package jsonl

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"os"
)

// Record is one complete newline-terminated JSONL record and its source
// position. LineNumber is one-based; EndOffset is exclusive.
type Record struct {
	Raw         []byte
	LineNumber  int64
	StartOffset int64
	EndOffset   int64
}

// Position identifies the next record to read. LineNumber is the number of
// complete records before ByteOffset.
type Position struct {
	ByteOffset int64
	LineNumber int64
}

// ReadComplete reads newline-terminated records starting at offset. It returns
// the byte offset immediately after the last complete record and deliberately
// ignores a trailing partial record.
func ReadComplete(path string, offset int64, visit func([]byte)) (int64, error) {
	position, err := ReadPositioned(path, Position{ByteOffset: offset}, func(record Record) {
		visit(record.Raw)
	})
	return position.ByteOffset, err
}

// ReadPositioned reads complete records with line and byte positions. A
// trailing partial record is deliberately left for a later read.
func ReadPositioned(path string, position Position, visit func(Record)) (Position, error) {
	file, err := os.Open(path)
	if err != nil {
		return position, fmt.Errorf("open JSONL file %q: %w", path, err)
	}
	defer file.Close()
	return ReadPositionedFile(file, position, visit)
}

// ReadCompleteFile reads complete records from an already-open descriptor.
func ReadCompleteFile(file *os.File, offset int64, visit func([]byte)) (int64, error) {
	position, err := ReadPositionedFile(file, Position{ByteOffset: offset}, func(record Record) {
		visit(record.Raw)
	})
	return position.ByteOffset, err
}

// ReadPositionedFile is ReadPositioned for an already-open descriptor.
func ReadPositionedFile(file *os.File, position Position, visit func(Record)) (Position, error) {
	if position.ByteOffset < 0 || position.LineNumber < 0 {
		return position, fmt.Errorf("invalid JSONL position: offset and line number must be non-negative")
	}
	if _, err := file.Seek(position.ByteOffset, io.SeekStart); err != nil {
		return position, fmt.Errorf("seek JSONL file %q: %w", file.Name(), err)
	}

	reader := bufio.NewReader(file)
	complete := position
	for {
		line, readErr := reader.ReadBytes('\n')
		if len(line) > 0 && line[len(line)-1] == '\n' {
			start := complete.ByteOffset
			complete.ByteOffset += int64(len(line))
			complete.LineNumber++
			visit(Record{
				Raw:         line,
				LineNumber:  complete.LineNumber,
				StartOffset: start,
				EndOffset:   complete.ByteOffset,
			})
		}
		if readErr == nil {
			continue
		}
		if errors.Is(readErr, io.EOF) {
			return complete, nil
		}
		return complete, fmt.Errorf("read JSONL file %q: %w", file.Name(), readErr)
	}
}
