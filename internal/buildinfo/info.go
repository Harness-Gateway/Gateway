// Package buildinfo identifies the gateway and its harness protocol clients.
package buildinfo

import (
	"fmt"
	"runtime"
	"runtime/debug"
)

const Version = "0.1.1"

func String() string {
	revision := "unknown"
	modified := false
	if info, ok := debug.ReadBuildInfo(); ok {
		// Versioned go install builds come from a module zip, without Git metadata.
		if info.Main.Version != "" && info.Main.Version != "(devel)" {
			revision = "module " + info.Main.Version
		}
		for _, setting := range info.Settings {
			switch setting.Key {
			case "vcs.revision":
				revision = setting.Value
			case "vcs.modified":
				modified = setting.Value == "true"
			}
		}
	}
	if modified {
		revision += "+dirty"
	}
	return fmt.Sprintf("Harness-Gateway v%s (%s; %s/%s)", Version, revision, runtime.GOOS, runtime.GOARCH)
}
