package p2p

import (
	"encoding/json"
	"testing"
)

func TestCanonicalRPCResultNormalizesObjectOrderAndWhitespace(t *testing.T) {
	left, err := canonicalRPCResult(json.RawMessage(`{"height": 10, "hash": "abc"}`))
	if err != nil {
		t.Fatal(err)
	}
	right, err := canonicalRPCResult(json.RawMessage(`{"hash":"abc","height":10}`))
	if err != nil {
		t.Fatal(err)
	}
	if string(left) != string(right) {
		t.Fatalf("canonical results differ: %s != %s", left, right)
	}
}

func TestCanonicalRPCResultPreservesLargeInteger(t *testing.T) {
	canonical, err := canonicalRPCResult(json.RawMessage(`900719925474099312345`))
	if err != nil {
		t.Fatal(err)
	}
	if string(canonical) != "900719925474099312345" {
		t.Fatalf("large integer changed: %s", canonical)
	}
}

func TestCanonicalRPCResultRejectsTrailingJSON(t *testing.T) {
	if _, err := canonicalRPCResult(json.RawMessage(`{} {}`)); err == nil {
		t.Fatal("multiple JSON values were accepted")
	}
}
