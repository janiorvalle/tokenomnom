// Package testcorpus builds deterministic, corpus-shaped history fixtures for
// benchmarks. It deliberately contains no database or indexer dependencies so
// both packages can consume the same generated shape without import cycles.
package testcorpus

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/janiorvalle/tokenomnom/internal/discover"
	"github.com/janiorvalle/tokenomnom/internal/history"
)

const (
	DefaultSessions = 5500
	DefaultPrompts  = 27000
	DefaultSeed     = uint64(44)
)

// Spec controls the deterministic corpus size and content sequence.
type Spec struct {
	Sessions int
	Prompts  int
	Seed     uint64
}

// DefaultSpec approximates the field corpus used to motivate PR 16.
func DefaultSpec() Spec {
	return Spec{Sessions: DefaultSessions, Prompts: DefaultPrompts, Seed: DefaultSeed}
}

// Corpus is a provider-neutral synthetic history corpus.
type Corpus struct {
	Sessions []Session
	Prompts  int
}

// Session contains one synthetic logical session and its source occurrence mix.
type Session struct {
	Index          int
	Provider       history.Provider
	NativeID       string
	CWD            string
	RepositoryName string
	Branch         string
	ThreadKind     history.ThreadKind
	FirstTimestamp time.Time
	Prompts        []Prompt
	Live           bool
	Vault          bool
	Archived       bool
}

// Prompt is one deterministic searchable user prompt.
type Prompt struct {
	NativeID  string
	Text      string
	Timestamp time.Time
}

// Generate creates the in-memory benchmark corpus without reading the clock or
// process-global random state.
func Generate(spec Spec) Corpus {
	if spec.Sessions <= 0 {
		spec.Sessions = DefaultSessions
	}
	if spec.Prompts < spec.Sessions {
		spec.Prompts = spec.Sessions
	}
	if spec.Seed == 0 {
		spec.Seed = DefaultSeed
	}
	basePrompts, extraPrompts := spec.Prompts/spec.Sessions, spec.Prompts%spec.Sessions
	baseTime := time.Date(2025, 1, 6, 8, 0, 0, 0, time.UTC)
	result := Corpus{Sessions: make([]Session, 0, spec.Sessions), Prompts: spec.Prompts}
	promptIndex := 0
	for index := range spec.Sessions {
		provider := history.ProviderClaude
		if index%5 < 3 {
			provider = history.ProviderCodex
		}
		cwdProject := fmt.Sprintf("project-%03d", index%480)
		session := Session{
			Index: index, Provider: provider, NativeID: fmt.Sprintf("corpus-%06d", index),
			CWD:    filepath.ToSlash(filepath.Join("/workspace", fmt.Sprintf("team-%02d", index%32), cwdProject)),
			Branch: fmt.Sprintf("branch-%02d", index%36), ThreadKind: threadKind(index),
			FirstTimestamp: baseTime.Add(time.Duration(index%540)*24*time.Hour + time.Duration(index%24)*time.Hour),
			Live:           index%10 != 0, Vault: index%10 < 3, Archived: index%17 == 0,
		}
		if provider == history.ProviderCodex && index%4 != 0 {
			session.RepositoryName = fmt.Sprintf("repo-%03d", (index/5)%260)
		}
		count := basePrompts
		if index < extraPrompts {
			count++
		}
		session.Prompts = make([]Prompt, 0, count)
		for promptOffset := range count {
			length := promptLength(spec.Seed, promptIndex)
			text := promptText(promptIndex, length)
			if promptIndex%137 == 0 {
				text = "needle " + text
			}
			session.Prompts = append(session.Prompts, Prompt{
				NativeID: fmt.Sprintf("prompt-%07d", promptIndex), Text: text,
				Timestamp: session.FirstTimestamp.Add(time.Duration(promptOffset+1) * time.Minute),
			})
			promptIndex++
		}
		result.Sessions = append(result.Sessions, session)
	}
	return result
}

func threadKind(index int) history.ThreadKind {
	switch index % 100 {
	case 0, 1, 2, 3, 4, 5, 6, 7:
		return history.ThreadSubagent
	case 8, 9, 10, 11, 12, 13, 14, 15, 16, 17, 18, 19, 20, 21, 22, 23, 24, 25, 26:
		return history.ThreadUnknown
	default:
		return history.ThreadRoot
	}
}

func promptLength(seed uint64, index int) int {
	value := mix(seed + uint64(index)*0x9e3779b97f4a7c15)
	bucket := value % 100
	switch {
	case bucket < 60:
		return 40 + int(value%161)
	case bucket < 85:
		return 201 + int(value%800)
	case bucket < 95:
		return 1001 + int(value%3000)
	case bucket < 99:
		return 4001 + int(value%12000)
	default:
		return 16001 + int(value%48000)
	}
}

func mix(value uint64) uint64 {
	value ^= value >> 30
	value *= 0xbf58476d1ce4e5b9
	value ^= value >> 27
	value *= 0x94d049bb133111eb
	return value ^ (value >> 31)
}

func promptText(index, length int) string {
	prefix := fmt.Sprintf("corpus prompt %07d ", index)
	if length <= len(prefix) {
		return prefix[:length]
	}
	const filler = "alpha beta gamma delta epsilon zeta eta theta iota kappa lambda "
	var builder strings.Builder
	builder.Grow(length)
	builder.WriteString(prefix)
	for builder.Len() < length {
		remaining := length - builder.Len()
		if remaining < len(filler) {
			builder.WriteString(filler[:remaining])
			break
		}
		builder.WriteString(filler)
	}
	return builder.String()
}

// WriteLiveFiles materializes the generated sessions as valid Codex and Claude
// JSONL sources for end-to-end incremental index benchmarks.
func WriteLiveFiles(root string, corpus Corpus) ([]discover.Root, error) {
	codexRoot := filepath.Join(root, "codex")
	claudeRoot := filepath.Join(root, "claude")
	for _, session := range corpus.Sessions {
		if err := writeSession(codexRoot, claudeRoot, session); err != nil {
			return nil, err
		}
	}
	return []discover.Root{
		{Provider: discover.ProviderCodex, Path: codexRoot, Source: "benchmark", Exists: true},
		{Provider: discover.ProviderClaude, Path: claudeRoot, Source: "benchmark", Exists: true},
	}, nil
}

func writeSession(codexRoot, claudeRoot string, session Session) error {
	path := ""
	if session.Provider == history.ProviderCodex {
		subtree := "sessions"
		if session.Archived {
			subtree = "archived_sessions"
		}
		path = filepath.Join(codexRoot, subtree, "2026", "01", session.NativeID+".jsonl")
	} else {
		path = filepath.Join(claudeRoot, "projects", fmt.Sprintf("project-%03d", session.Index%480), session.NativeID+".jsonl")
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	file, err := os.OpenFile(path, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	writer := bufio.NewWriterSize(file, 64*1024)
	encode := func(value any) error {
		encoded, err := json.Marshal(value)
		if err != nil {
			return err
		}
		if _, err := writer.Write(encoded); err != nil {
			return err
		}
		return writer.WriteByte('\n')
	}
	if session.Provider == history.ProviderCodex {
		payload := map[string]any{"id": session.NativeID, "cwd": session.CWD, "thread_source": string(session.ThreadKind)}
		if session.RepositoryName != "" {
			payload["git"] = map[string]any{"branch": session.Branch, "repository_url": "https://example.invalid/acme/" + session.RepositoryName + ".git"}
		}
		err = encode(map[string]any{"timestamp": session.FirstTimestamp.Format(time.RFC3339), "type": "session_meta", "payload": payload})
	}
	for _, prompt := range session.Prompts {
		if err != nil {
			break
		}
		if session.Provider == history.ProviderCodex {
			err = encode(map[string]any{"timestamp": prompt.Timestamp.Format(time.RFC3339), "type": "event_msg", "payload": map[string]any{"type": "user_message", "client_id": prompt.NativeID, "message": prompt.Text}})
		} else {
			err = encode(map[string]any{"type": "user", "uuid": prompt.NativeID, "sessionId": session.NativeID, "cwd": session.CWD, "timestamp": prompt.Timestamp.Format(time.RFC3339), "message": map[string]any{"role": "user", "content": prompt.Text}})
		}
	}
	if err == nil {
		err = writer.Flush()
	}
	closeErr := file.Close()
	if err != nil {
		return err
	}
	return closeErr
}
