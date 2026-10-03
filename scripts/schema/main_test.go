package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestPublishedSchemasMatchWireModels(t *testing.T) {
	for name, document := range documents() {
		data, err := json.MarshalIndent(document, "", "  ")
		if err != nil {
			t.Fatal(err)
		}
		saved, err := os.ReadFile(filepath.Join("..", "..", "schemas", name+".schema.json"))
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(saved, append(data, '\n')) {
			t.Fatalf("%s schema drifted; regenerate with go run ./scripts/schema", name)
		}
	}
}
