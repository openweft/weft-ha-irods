// Package keyinstall puts the zone keys held in the DCS into the iRODS
// server's own configuration file.
//
// iRODS 5 reads its keys only as plain JSON properties of
// /etc/irods/server_config.json (schemas/configuration/v5/server_config.json.in
// in irods/irods): "negotiation_key" (exactly 32 characters of [A-Za-z0-9_])
// and "zone_key" (at most 49 characters of the same set). The schema has no
// control-plane key property: the iRODS 4 control plane no longer exists, so
// that third key is not installed anywhere.
//
// The file is rewritten atomically (temp file in the same directory, fsync,
// rename), mode 0600, owned by the iRODS service account; every other field is
// preserved. Key values never appear in an error and are never written to any
// other file.
package keyinstall

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
)

// Keys are the zone keys iRODS reads from server_config.json.
type Keys struct {
	Negotiation string
	Zone        string
}

const (
	fieldNegotiation = "negotiation_key"
	fieldZone        = "zone_key"

	negotiationLen = 32
	zoneKeyMaxLen  = 49
)

// validate checks the keys against the iRODS 5 schema. The messages name the
// field and the rule, never the value.
func (k Keys) validate() error {
	if len(k.Negotiation) != negotiationLen || !keyChars(k.Negotiation) {
		return fmt.Errorf("%s must be exactly %d characters of [A-Za-z0-9_]", fieldNegotiation, negotiationLen)
	}
	if k.Zone == "" || len(k.Zone) > zoneKeyMaxLen || !keyChars(k.Zone) {
		return fmt.Errorf("%s must be 1 to %d characters of [A-Za-z0-9_]", fieldZone, zoneKeyMaxLen)
	}
	return nil
}

func keyChars(s string) bool {
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9', c == '_':
		default:
			return false
		}
	}
	return true
}

// merge sets the two key properties in the JSON object doc and returns the new
// document. changed is false when doc already holds exactly these values (out
// is then nil). Every other property is carried over byte for byte (as
// compacted JSON); the order of properties is not preserved.
func merge(doc []byte, k Keys) (out []byte, changed bool, err error) {
	if err := k.validate(); err != nil {
		return nil, false, err
	}
	var obj map[string]json.RawMessage
	if err := json.Unmarshal(doc, &obj); err != nil {
		// encoding/json quotes the offending character, which in this file
		// could be part of a key: report the position only.
		var syn *json.SyntaxError
		if errors.As(err, &syn) {
			return nil, false, fmt.Errorf("server config is not valid JSON (offset %d)", syn.Offset)
		}
		return nil, false, errors.New("server config is not a JSON object")
	}
	if obj == nil {
		return nil, false, errors.New("server config is not a JSON object")
	}
	neg, _ := json.Marshal(k.Negotiation)
	zone, _ := json.Marshal(k.Zone)
	if bytes.Equal(obj[fieldNegotiation], neg) && bytes.Equal(obj[fieldZone], zone) {
		return nil, false, nil
	}
	obj[fieldNegotiation] = neg
	obj[fieldZone] = zone

	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	enc.SetIndent("", "    ")
	// Cannot fail: every value is valid JSON (RawMessages the decoder just
	// accepted, or two strings) and a bytes.Buffer does not refuse writes.
	_ = enc.Encode(obj)
	return buf.Bytes(), true, nil
}
