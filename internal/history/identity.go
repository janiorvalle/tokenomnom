package history

import (
	"crypto/sha256"
	"encoding/hex"
	"strings"
	"time"
)

// SessionIdentityKey prefers a provider-native ID and otherwise uses the
// immutable first complete record. The source path is the last-resort key for
// empty files.
func SessionIdentityKey(provider Provider, nativeID, path string, firstRecord []byte) (identityKey, fallbackKey string) {
	if nativeID = strings.TrimSpace(nativeID); nativeID != "" {
		return "native:" + nativeID, ""
	}
	if len(firstRecord) > 0 {
		fallbackKey = "first-record:" + digest(firstRecord)
	} else {
		fallbackKey = "source-path:" + path
	}
	return "fallback:" + fallbackKey, fallbackKey
}

// TimestampedMessageIdentityKey reconciles provider records that encode one
// logical message more than once with the same timestamp and text.
func TimestampedMessageIdentityKey(timestamp string, lineNumber int64, text string) string {
	if parsed, err := time.Parse(time.RFC3339Nano, timestamp); err == nil {
		return "timestamp:" + parsed.UTC().Format(time.RFC3339Nano) + ":" + digest([]byte(text))
	}
	return "record:" + digest([]byte(text)) + ":" + decimal(lineNumber)
}

// MessageIdentityKey prefers a provider-native message ID.
func MessageIdentityKey(nativeID string, lineNumber int64, text string) string {
	if nativeID = strings.TrimSpace(nativeID); nativeID != "" {
		return "native:" + nativeID
	}
	return "record:" + digest([]byte(text)) + ":" + decimal(lineNumber)
}

func digest(value []byte) string {
	sum := sha256.Sum256(value)
	return hex.EncodeToString(sum[:])
}

func decimal(value int64) string {
	if value == 0 {
		return "0"
	}
	negative := value < 0
	if negative {
		value = -value
	}
	var buffer [20]byte
	i := len(buffer)
	for value > 0 {
		i--
		buffer[i] = byte('0' + value%10)
		value /= 10
	}
	if negative {
		i--
		buffer[i] = '-'
	}
	return string(buffer[i:])
}
