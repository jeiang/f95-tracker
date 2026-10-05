package domain

import (
	"encoding/json"
	"os"
	"testing"
)

type Vectors struct {
	Normalize []struct {
		Name string `json:"name"`
		In   string `json:"in"`
		Want string `json:"want"`
	} `json:"normalize"`
	Behind []struct {
		Name   string `json:"name"`
		Latest string `json:"latest"`
		Played string `json:"played"`
		Behind bool   `json:"behind"`
	} `json:"behind"`
}

func TestNormalizeVersionVectors(t *testing.T) {
	raw, err := os.ReadFile("testdata/version_vectors.json")
	if err != nil {
		t.Fatal(err)
	}
	var v Vectors
	if err := json.Unmarshal(raw, &v); err != nil {
		t.Fatal(err)
	}
	for _, c := range v.Normalize {
		t.Run(c.Name, func(t *testing.T) {
			if got := NormalizeVersion(c.In); got != c.Want {
				t.Fatalf("NormalizeVersion(%q) = %q, want %q", c.In, got, c.Want)
			}
		})
	}
	for _, c := range v.Behind {
		t.Run("behind/"+c.Name, func(t *testing.T) {
			if got := NormalizeVersion(c.Latest) != NormalizeVersion(c.Played); got != c.Behind {
				t.Fatalf("behind(%q,%q) = %v, want %v", c.Latest, c.Played, got, c.Behind)
			}
		})
	}
}

func TestEnumValid(t *testing.T) {
	if !PlayOnHold.Valid() || PlayStatus("on hold").Valid() || PlayStatus("").Valid() {
		t.Fatal("PlayStatus.Valid")
	}
	if !JobNeedsHuman.Valid() || JobState("failed").Valid() {
		t.Fatal("JobState.Valid")
	}
}
