package parental

import (
	"encoding/json"
	"os"
	"testing"
)

// Golden digests come from the previously qualified Python decoder, not from
// the Go implementation. They pin the cross-language canonical representation.
func TestCatalogLaboratoryParity(t *testing.T) {
	data, err := os.ReadFile("testdata/classifier-vectors.json")
	if err != nil {
		t.Fatal(err)
	}
	var vectors []struct {
		Name    string
		Catalog json.RawMessage
		Digest  string
	}
	if err = json.Unmarshal(data, &vectors); err != nil {
		t.Fatal(err)
	}
	if len(vectors) != 3 {
		t.Fatal("missing parity vectors")
	}
	for _, v := range vectors {
		t.Run(v.Name, func(t *testing.T) {
			c, err := DecodeCatalog(v.Catalog)
			if err != nil {
				t.Fatal(err)
			}
			if c.Digest() != v.Digest {
				t.Fatal("canonical digest differs from qualified model")
			}
		})
	}
}
