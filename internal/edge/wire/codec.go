// Package wire holds how the public contract is read off the wire.
package wire

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
)

// ErrNotProto marks a value that is not a protobuf message.
var ErrNotProto = errors.New("not a protobuf message")

// StrictJSON is the JSON codec for the public contract.
//
// connect-go's built-in JSON codec decodes with DiscardUnknown, and in
// protojson that option ALSO drops unknown enum NAMES: the field is left at
// zero instead of failing (protojson decode.go, unmarshalEnum). For fields
// where zero is a valid choice - craving_type and social_context mean
// "none" at zero - a mistyped name would silently become "none", and the
// guide would ignore exactly what the user asked for. That is the bug the
// REST gateway refused explicitly, and this codec keeps refusing it.
//
// Unknown FIELD names are refused for the same reason: the only client of
// this contract is ours, released in step with it, and a misspelled field is
// a bug to surface, not a value to ignore.
//
// Encoding is protojson's default: lowerCamelCase field names, enums by
// name, and unset fields omitted.
type StrictJSON struct {
	name string
}

var _ connect.Codec = StrictJSON{}

// Codecs returns the strict codec under both names a Connect client may
// send in its Content-Type.
func Codecs() []connect.HandlerOption {
	return []connect.HandlerOption{
		connect.WithCodec(StrictJSON{name: "json"}),
		connect.WithCodec(StrictJSON{name: "json; charset=utf-8"}),
	}
}

func (c StrictJSON) Name() string { return c.name }

func (StrictJSON) IsBinary() bool { return false }

func (StrictJSON) Marshal(message any) ([]byte, error) {
	msg, ok := message.(proto.Message)
	if !ok {
		return nil, fmt.Errorf("%w: %T", ErrNotProto, message)
	}
	return protojson.MarshalOptions{}.Marshal(msg)
}

func (StrictJSON) Unmarshal(data []byte, message any) error {
	msg, ok := message.(proto.Message)
	if !ok {
		return fmt.Errorf("%w: %T", ErrNotProto, message)
	}
	if len(data) == 0 {
		return errors.New("zero-length payload is not a valid JSON object")
	}
	if err := (protojson.UnmarshalOptions{DiscardUnknown: false}).Unmarshal(data, msg); err != nil {
		return fmt.Errorf("unmarshal into %T: %w", message, err)
	}
	return nil
}

// MarshalStable is used for GET requests, where the message becomes part of
// the URL and has to be byte-for-byte repeatable to be cacheable.
func (c StrictJSON) MarshalStable(message any) ([]byte, error) {
	out, err := c.Marshal(message)
	if err != nil {
		return nil, err
	}
	var compact bytes.Buffer
	if err := json.Compact(&compact, out); err != nil {
		return nil, err
	}
	return compact.Bytes(), nil
}
