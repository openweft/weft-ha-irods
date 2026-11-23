package keyinstall

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
)

func TestMergeUnchangedWhenAlreadyInstalled(t *testing.T) {
	k := Keys{Negotiation: strings.Repeat("a", 32), Zone: "z"}
	doc := `{"negotiation_key":"` + k.Negotiation + `","zone_key":"z","x":1}`
	out, changed, err := merge([]byte(doc), k)
	if err != nil || changed || out != nil {
		t.Fatalf("merge = %q, %v, %v", out, changed, err)
	}
}

func TestMergeKeepsEveryOtherField(t *testing.T) {
	k := Keys{Negotiation: strings.Repeat("a", 32), Zone: "z"}
	out, changed, err := merge([]byte(`{"a":1,"b":{"c":[true,null]},"zone_key":"old"}`), k)
	if err != nil || !changed {
		t.Fatalf("merge = %v, %v", changed, err)
	}
	var m map[string]json.RawMessage
	if err := json.Unmarshal(out, &m); err != nil {
		t.Fatal(err)
	}
	if string(m["a"]) != "1" || string(m["zone_key"]) != `"z"` || !bytes.Contains(m["b"], []byte("null")) {
		t.Fatalf("got %s", out)
	}
}

func TestMergeErrorNeverQuotesTheDocument(t *testing.T) {
	k := Keys{Negotiation: strings.Repeat("a", 32), Zone: "z"}
	_, _, err := merge([]byte(`{"negotiation_key": S3CRET}`), k)
	if err == nil || strings.Contains(err.Error(), "'S'") || strings.Contains(err.Error(), "S3CRET") {
		t.Fatalf("err = %v", err)
	}
	if _, _, err := merge([]byte(`{"a":1}x`), k); err == nil {
		t.Fatal("trailing garbage accepted")
	}
	if _, _, err := merge([]byte(`"str"`), k); err == nil {
		t.Fatal("non-object accepted")
	}
}

func FuzzMerge(f *testing.F) {
	for _, s := range []string{`{}`, `{"a":1}`, `null`, `[]`, `{"zone_key":"x"}`, `{"a":`, "\xff", `{"a":"\u0000"}`, `{"negotiation_key":1}`} {
		f.Add([]byte(s))
	}
	k := Keys{Negotiation: strings.Repeat("a", 32), Zone: "zk"}
	f.Fuzz(func(t *testing.T, doc []byte) {
		out, changed, err := merge(doc, k)
		if err != nil {
			if out != nil || changed {
				t.Fatal("error with output")
			}
			// An error must never quote the document.
			if len(doc) > 3 && bytes.Contains([]byte(err.Error()), doc) {
				t.Fatalf("error quotes the input: %v", err)
			}
			return
		}
		if !changed {
			return
		}
		var m map[string]json.RawMessage
		if err := json.Unmarshal(out, &m); err != nil {
			t.Fatalf("output is not a JSON object: %v", err)
		}
		if string(m["negotiation_key"]) != `"`+k.Negotiation+`"` || string(m["zone_key"]) != `"zk"` {
			t.Fatalf("keys not set: %s", out)
		}
		// Every property of the input survives, value for value.
		var in map[string]json.RawMessage
		if err := json.Unmarshal(doc, &in); err != nil {
			t.Fatal(err)
		}
		for key, v := range in {
			if key == "negotiation_key" || key == "zone_key" {
				continue
			}
			var a, b bytes.Buffer
			if json.Compact(&a, v) != nil || json.Compact(&b, m[key]) != nil || !jsonEqual(a.Bytes(), b.Bytes()) {
				t.Fatalf("property %q changed: %s -> %s", key, v, m[key])
			}
		}
		// Idempotent.
		if _, again, err := merge(out, k); err != nil || again {
			t.Fatalf("second merge = %v, %v", again, err)
		}
	})
}

// jsonEqual compares two compact JSON values; HTML escapes are decoded by
// round-tripping through any.
func jsonEqual(a, b []byte) bool {
	var x, y any
	if json.Unmarshal(a, &x) != nil || json.Unmarshal(b, &y) != nil {
		return false
	}
	xa, _ := json.Marshal(x)
	yb, _ := json.Marshal(y)
	return bytes.Equal(xa, yb)
}
