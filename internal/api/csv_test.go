package api

import (
	"strings"
	"testing"
)

func TestParseTargetCSVWithHeader(t *testing.T) {
	csv := "first_name,last_name,email,position,department\n" +
		"Alice,Ng,alice@example.com,CFO,Finance\n" +
		"Bob,,bob@example.com,,IT\n"
	targets, err := ParseTargetCSV(strings.NewReader(csv))
	if err != nil {
		t.Fatal(err)
	}
	if len(targets) != 2 {
		t.Fatalf("want 2 recipients, got %d", len(targets))
	}
	if targets[0].FirstName != "Alice" || targets[0].Position != "CFO" {
		t.Fatalf("unexpected first row: %+v", targets[0])
	}
	if targets[0].Custom["department"] != "Finance" {
		t.Fatalf("extra column not kept as custom field: %+v", targets[0].Custom)
	}
}

func TestParseTargetCSVWithoutHeader(t *testing.T) {
	csv := "Alice,Ng,alice@example.com,CFO\nBob,Lee,bob@example.com,Engineer\n"
	targets, err := ParseTargetCSV(strings.NewReader(csv))
	if err != nil {
		t.Fatal(err)
	}
	if len(targets) != 2 || targets[1].LastName != "Lee" || targets[1].Email != "bob@example.com" {
		t.Fatalf("unexpected parse: %+v", targets)
	}
}

func TestParseTargetCSVEdgeCases(t *testing.T) {
	// UTF-8 BOM, duplicate address, invalid row, reordered columns.
	csv := "\xEF\xBB\xBFEmail,First Name,Last Name\n" +
		"alice@example.com,Alice,Ng\n" +
		"ALICE@example.com,Alice,Duplicate\n" +
		"not-an-email,Bad,Row\n"
	targets, err := ParseTargetCSV(strings.NewReader(csv))
	if err != nil {
		t.Fatal(err)
	}
	if len(targets) != 1 {
		t.Fatalf("want duplicates and invalid rows dropped, got %+v", targets)
	}
	if targets[0].Email != "alice@example.com" || targets[0].FirstName != "Alice" {
		t.Fatalf("header names not matched loosely: %+v", targets[0])
	}
}

func TestParseTargetCSVEmpty(t *testing.T) {
	if _, err := ParseTargetCSV(strings.NewReader("")); err == nil {
		t.Fatal("empty CSV should error")
	}
	if _, err := ParseTargetCSV(strings.NewReader("name,role\nx,y\n")); err == nil {
		t.Fatal("CSV without an email column should error")
	}
}
