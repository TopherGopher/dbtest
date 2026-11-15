package mongod

import (
	"bytes"
	"encoding/binary"
	"math"
	"strings"
	"unicode/utf8"
)

// This is just enough BSON to read mongod's reply to a hello: the top-level
// fields of one document. Every length comes from the peer, so each is
// checked against the bytes that are really there before it is used.

// emptyDocumentLen is a document with no fields: its length and a zero.
const emptyDocumentLen = 5

// The kinds of BSON element, by the byte that introduces each.
const (
	bsonDouble        = 0x01
	bsonString        = 0x02
	bsonDocument      = 0x03
	bsonArray         = 0x04
	bsonBinary        = 0x05
	bsonUndefined     = 0x06
	bsonObjectID      = 0x07
	bsonBool          = 0x08
	bsonDateTime      = 0x09
	bsonNull          = 0x0A
	bsonRegex         = 0x0B
	bsonDBPointer     = 0x0C
	bsonJavaScript    = 0x0D
	bsonSymbol        = 0x0E
	bsonCodeWithScope = 0x0F
	bsonInt32         = 0x10
	bsonTimestamp     = 0x11
	bsonInt64         = 0x12
	bsonDecimal128    = 0x13
	bsonMaxKey        = 0x7F
	bsonMinKey        = 0xFF
)

// documentElements returns a document's elements, without its length and
// closing zero. It reports false if the length it states is not one the
// bytes can hold.
func documentElements(doc []byte) ([]byte, bool) {
	length, ok := lengthPrefix(doc)
	if !ok || length < emptyDocumentLen || length > len(doc) {
		return nil, false
	}
	if doc[length-1] != 0 {
		return nil, false
	}
	return doc[4 : length-1], true
}

// eachElement calls visit for every element, and reports whether the
// elements were well formed to the end. It never looks inside a value.
func eachElement(elements []byte, visit func(kind byte, name string, value []byte)) bool {
	for len(elements) > 0 {
		kind := elements[0]
		name, rest, ok := cString(elements[1:])
		if !ok {
			return false
		}
		size, ok := valueSize(kind, rest)
		if !ok || size > len(rest) {
			return false
		}
		visit(kind, name, rest[:size])
		elements = rest[size:]
	}
	return true
}

// valueSize returns how many bytes an element's value takes, and false for
// a kind BSON does not have or a length the value cannot have.
func valueSize(kind byte, value []byte) (int, bool) {
	switch kind {
	case bsonUndefined, bsonNull, bsonMinKey, bsonMaxKey:
		return 0, true
	case bsonBool:
		return 1, true
	case bsonInt32:
		return 4, true
	case bsonDouble, bsonDateTime, bsonTimestamp, bsonInt64:
		return 8, true
	case bsonObjectID:
		return 12, true
	case bsonDecimal128:
		return 16, true
	case bsonString, bsonJavaScript, bsonSymbol:
		return prefixedSize(value, 4, 1)
	case bsonDocument, bsonArray, bsonCodeWithScope:
		return prefixedSize(value, 0, emptyDocumentLen)
	case bsonBinary:
		return prefixedSize(value, 5, 0)
	case bsonDBPointer:
		return dbPointerSize(value)
	case bsonRegex:
		return regexSize(value)
	}
	return 0, false
}

// prefixedSize returns the size of a value that starts with its own length:
// that length, which must be at least least, plus extra bytes the length
// does not count.
func prefixedSize(value []byte, extra, least int) (int, bool) {
	length, ok := lengthPrefix(value)
	if !ok || length < least {
		return 0, false
	}
	return length + extra, true
}

// lengthPrefix reads the int32 a value or document starts with. A negative
// length, or one too large to be an offset into anything read here, is
// refused: it would overflow the sums made with it.
func lengthPrefix(b []byte) (int, bool) {
	if len(b) < 4 {
		return 0, false
	}
	length := binary.LittleEndian.Uint32(b)
	if length > maxReplyLen {
		return 0, false
	}
	return int(length), true
}

// dbPointerSize is a string followed by a twelve-byte id.
func dbPointerSize(value []byte) (int, bool) {
	const idLen = 12
	size, ok := prefixedSize(value, 4, 1)
	return size + idLen, ok
}

// regexSize is two strings that each end in a zero.
func regexSize(value []byte) (int, bool) {
	_, afterPattern, ok := cString(value)
	if !ok {
		return 0, false
	}
	_, afterOptions, ok := cString(afterPattern)
	return len(value) - len(afterOptions), ok
}

// cString splits off a string that ends in a zero byte.
func cString(b []byte) (s string, rest []byte, ok bool) {
	end := bytes.IndexByte(b, 0)
	if end < 0 {
		return "", nil, false
	}
	return string(b[:end]), b[end+1:], true
}

// isOne reports whether a numeric or boolean value is 1, which is how a
// server says a command succeeded. Different versions use different kinds.
func isOne(kind byte, value []byte) bool {
	le := binary.LittleEndian
	switch kind {
	case bsonDouble:
		return math.Float64frombits(le.Uint64(value)) == 1
	case bsonInt32:
		return le.Uint32(value) == 1
	case bsonInt64:
		return le.Uint64(value) == 1
	case bsonBool:
		return value[0] == 1
	}
	return false
}

// stringValue returns a string element's text, or "" for any other kind.
// The value is its length, the text, and a closing zero.
func stringValue(kind byte, value []byte) string {
	if kind != bsonString {
		return ""
	}
	return string(value[4 : len(value)-1])
}

// truncate cuts s to at most limit bytes without splitting a character,
// after dropping anything that is not valid text: s came from a peer.
func truncate(s string, limit int) string {
	s = strings.ToValidUTF8(s, "")
	if len(s) <= limit {
		return s
	}
	cut := limit
	for cut > 0 && !utf8.RuneStart(s[cut]) {
		cut--
	}
	return s[:cut]
}
