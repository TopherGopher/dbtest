package dockerapi

import (
	"encoding/json"
	"errors"
	"io"
)

// errTrailingData is a reply with more after its JSON value.
var errTrailingData = errors.New("the reply has more after its JSON value")

// decodeReply decodes the one JSON value a reply holds, and reads on to the
// end of it. A body read to its end lets the HTTP client reuse the
// connection; the decoder alone stops after the value.
func decodeReply(r io.Reader, v any) error {
	dec := json.NewDecoder(r)
	if err := dec.Decode(v); err != nil {
		return err
	}
	if _, err := dec.Token(); err != io.EOF {
		return errTrailingData
	}
	return nil
}
