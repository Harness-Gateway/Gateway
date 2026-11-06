package main

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestAcceptanceLogRetention(t *testing.T) {
	failure := errors.New("acceptance failed")
	for _, tc := range []struct {
		name string
		keep bool
		err  error
	}{
		{"success", false, nil},
		{"failure", false, failure},
		{"interruption", false, context.Canceled},
		{"explicit retention", true, nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			directory, err := createLogsDirectory(t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			// Java-owned gateways also write to nested directories.
			nested := filepath.Join(directory, "gateway-example")
			if err := os.Mkdir(nested, 0700); err != nil {
				t.Fatal(err)
			}
			path := filepath.Join(nested, "gateway.log")
			if err := os.WriteFile(path, []byte("gateway diagnostic"), 0600); err != nil {
				t.Fatal(err)
			}
			if err := finishLogs(directory, tc.keep, tc.err); !errors.Is(err, tc.err) {
				t.Fatalf("run result changed: %v", err)
			}
			if tc.keep || tc.err != nil {
				data, err := os.ReadFile(path)
				if err != nil || string(data) != "gateway diagnostic" {
					t.Fatalf("gateway diagnostics were lost: data=%q error=%v", data, err)
				}
			} else if _, err := os.Stat(directory); !os.IsNotExist(err) {
				t.Fatalf("successful run left temporary logs: %v", err)
			}
		})
	}
}

func TestAcceptanceLogsUseSeparateRunDirectories(t *testing.T) {
	parent := filepath.Join(t.TempDir(), "diagnostics")
	first, err := createLogsDirectory(parent)
	if err != nil {
		t.Fatal(err)
	}
	second, err := createLogsDirectory(parent)
	if err != nil {
		t.Fatal(err)
	}
	if first == second || filepath.Dir(first) != parent || filepath.Dir(second) != parent {
		t.Fatalf("runs did not get separate directories under %s: %s %s", parent, first, second)
	}
	file := filepath.Join(t.TempDir(), "file")
	if err := os.WriteFile(file, nil, 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := createLogsDirectory(file); err == nil {
		t.Fatal("a file was accepted as an output directory")
	}
}
