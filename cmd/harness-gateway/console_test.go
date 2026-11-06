package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/harness-gateway/harness-gateway/internal/buildinfo"
)

func TestStartupPanelShowsActualConfiguration(t *testing.T) {
	status := startupStatus{
		baseURL: "http://[::1]:8099", directory: "/work/project with spaces", timeout: 45 * time.Second, concurrency: 4,
		harnesses: []harnessStatus{{name: "copilot", executable: "/opt/bin/copilot", model: "native-example", version: "9.8.7"}},
	}
	panel := startupPanel(status, false, 100)
	for _, want := range []string{
		"Harness-Gateway v" + buildinfo.Version, "LISTENING", buildinfo.RepositoryURL,
		"http://[::1]:8099/v1", "http://[::1]:8099/docs", "http://[::1]:8099/openapi.json", "http://[::1]:8099/health",
		"copilot", "9.8.7", "VERSION", "DEFAULT MODEL", "/opt/bin/copilot", "native-example", "/work/project with spaces",
		"4 concurrent requests / 45s timeout", "Press Ctrl+C to stop.",
	} {
		if !strings.Contains(panel, want) {
			t.Errorf("startup panel missing %q:\n%s", want, panel)
		}
	}
	if strings.Contains(panel, "\x1b") || strings.Contains(panel, "8080") || strings.Contains(panel, "codex") {
		t.Fatalf("startup panel contains ANSI codes, a fixed port, or a disabled harness:\n%s", panel)
	}
	status.harnesses[0].model = ""
	if !strings.Contains(startupPanel(status, false, 100), "CLI default") {
		t.Fatal("unset model is not identified as the CLI's configured default")
	}
}

func TestNarrowHarnessTablePreservesBothRowsAndPaths(t *testing.T) {
	harnesses := []harnessStatus{
		{name: "codex", executable: "/very/long/path with spaces/codex", version: "1.2.3"},
		{name: "copilot", executable: "/another/long/path/copilot", version: "4.5.6", model: "native-model"},
	}
	wide := harnessTable(harnesses, 160)
	if len(wide) != 3 || !strings.Contains(wide[0], "EXECUTABLE") {
		t.Fatalf("wide terminal did not get a four-column table: %v", wide)
	}
	narrow := harnessTable(harnesses, 40)
	if len(narrow) != 5 || strings.Contains(narrow[0], "EXECUTABLE") {
		t.Fatalf("narrow terminal did not move paths below the table rows: %v", narrow)
	}
	for i, harness := range harnesses {
		row := narrow[1+i*2]
		if !strings.Contains(row, harness.name) || !strings.Contains(row, harness.version) || strings.TrimSpace(narrow[2+i*2]) != harness.executable {
			t.Fatalf("narrow layout lost a harness or truncated its path: %v", narrow)
		}
	}
}

func TestRedirectedStartupLogsRemainMachineReadable(t *testing.T) {
	for _, format := range []string{"text", "json"} {
		t.Run(format, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "gateway.log")
			file, err := os.Create(path)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { file.Close() })
			status := startupStatus{baseURL: "http://127.0.0.1:43123", directory: "/work/project", timeout: time.Minute, concurrency: 2,
				harnesses: []harnessStatus{{name: "codex", version: "1.2.3"}}}
			if err := reportStartup(file, newLogger(file, format), format, status); err != nil {
				t.Fatal(err)
			}
			data, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			if strings.Count(string(data), "\n") != 1 || strings.Contains(string(data), "\x1b") || strings.Contains(string(data), "Ctrl+C") {
				t.Fatalf("redirected output includes a terminal panel or ANSI escapes: %s", data)
			}
			if format == "text" {
				if !strings.Contains(string(data), "url="+status.baseURL) {
					t.Fatalf("text startup record broke readiness discovery: %s", data)
				}
				return
			}
			var record map[string]any
			if err := json.Unmarshal(data, &record); err != nil {
				t.Fatal(err)
			}
			if record["harness_versions"].(map[string]any)["codex"] != "1.2.3" {
				t.Fatalf("JSON startup record lost the installed harness version: %s", data)
			}
			for key, want := range map[string]any{"msg": "Harness-Gateway listening", "version": buildinfo.Version, "repository": buildinfo.RepositoryURL,
				"url": status.baseURL, "api": status.baseURL + "/v1", "docs": status.baseURL + "/docs", "concurrency": float64(2)} {
				if record[key] != want {
					t.Errorf("JSON field %s=%v, want %v", key, record[key], want)
				}
			}
		})
	}
}
