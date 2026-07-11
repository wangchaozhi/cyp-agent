package main

import "testing"

func TestParseFloatList(t *testing.T) {
	values, err := parseFloatList("0.1, 0.2")
	if err != nil || len(values) != 2 || values[1] != 0.2 {
		t.Fatalf("parseFloatList() = %#v, %v", values, err)
	}
	if _, err := parseFloatList(" "); err == nil {
		t.Fatal("empty list unexpectedly succeeded")
	}
}
