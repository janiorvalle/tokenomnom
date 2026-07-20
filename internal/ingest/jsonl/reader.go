// Package jsonl provides checkpoint-aware reading of complete JSONL records.
package jsonl

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"os"
)

// ReadComplete reads newline-terminated records starting at offset. It returns
// the byte offset immediately after the last complete record and deliberately
// ignores a trailing partial record.
func ReadComplete(path string, offset int64, visit func([]byte)) (int64, error) {
	file, err := os.Open(path)
	if err != nil {
		return offset, fmt.Errorf("open JSONL file %q: %w", path, err)
	}
	defer file.Close()
	return ReadCompleteFile(file, offset, visit)
}

// ReadCompleteFile reads complete records from an already-open descriptor.
func ReadCompleteFile(file *os.File, offset int64, visit func([]byte)) (int64, error) {
	if _, err := file.Seek(offset, io.SeekStart); err != nil {
		return offset, fmt.Errorf("seek JSONL file %q: %w", file.Name(), err)
	}

	reader := bufio.NewReader(file)
	completeOffset := offset
	for {
		line, readErr := reader.ReadBytes('\n')
		if len(line) > 0 && line[len(line)-1] == '\n' {
			visit(line)
			completeOffset += int64(len(line))
		}
		if readErr == nil {
			continue
		}
		if errors.Is(readErr, io.EOF) {
			return completeOffset, nil
		}
		return completeOffset, fmt.Errorf("read JSONL file %q: %w", file.Name(), readErr)
	}
}
