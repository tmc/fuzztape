package fuzzfile

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

func TestRoundTrip(t *testing.T) {
	dir := t.TempDir()
	for _, data := range [][]byte{
		{},
		{0},
		[]byte("plain"),
		{0x00, 0x22, 0x5c, 0xff, '\n', '\t'},
		bytes.Repeat([]byte{0xde, 0xad}, 64),
	} {
		path, err := Write(dir, data)
		if err != nil {
			t.Fatalf("Write: %v", err)
		}
		base := filepath.Base(path)
		if !strings.HasPrefix(base, "fuzztape-") || len(base) != 24 {
			t.Errorf("filename = %q, want 24-char fuzztape-<hash>", base)
		}
		got, err := Read(path)
		if err != nil {
			t.Fatalf("Read: %v", err)
		}
		if !bytes.Equal(got, data) {
			t.Errorf("round trip = %x, want %x", got, data)
		}
	}
}

func TestReadRejectsInvalidShapes(t *testing.T) {
	dir := t.TempDir()
	for _, tc := range []struct {
		name      string
		content   string
		errSyntax bool
	}{
		{"wrong version", "go test fuzz v2\n[]byte(\"a\")\n", false},
		{"multiple values", "go test fuzz v1\nstring(\"a\")\nint(1)\n", false},
		{"not bytes", "go test fuzz v1\nstring(\"a\")\n", false},
		{"missing paren", "go test fuzz v1\n[]byte(\"a\"\n", false},
		{"unquotable", "go test fuzz v1\n[]byte(nope)\n", true},
		{"empty", "", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(dir, "seed")
			if err := os.WriteFile(path, []byte(tc.content), 0o644); err != nil {
				t.Fatal(err)
			}
			_, err := Read(path)
			if err == nil {
				t.Fatal("Read accepted invalid corpus record")
			}
			if tc.errSyntax && !errors.Is(err, strconv.ErrSyntax) {
				t.Errorf("Read error = %v, want wrapped strconv.ErrSyntax", err)
			}
		})
	}
}

func TestReadDir(t *testing.T) {
	dir := t.TempDir()
	entries, err := ReadDir(filepath.Join(dir, "absent"))
	if err != nil || entries != nil {
		t.Errorf("ReadDir(absent) = %v, %v; want nil, nil", entries, err)
	}

	file := filepath.Join(dir, "file")
	if err := os.WriteFile(file, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadDir(file); err == nil {
		t.Error("ReadDir(file) = nil error, want error")
	}

	sub := filepath.Join(dir, "sub")
	if _, err := Write(sub, []byte{1, 2, 3}); err != nil {
		t.Fatal(err)
	}
	entries, err = ReadDir(sub)
	if err != nil || len(entries) != 1 {
		t.Errorf("ReadDir(sub) = %d entries, %v; want 1, nil", len(entries), err)
	}
}
