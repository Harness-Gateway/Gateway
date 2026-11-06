// Package buildinfo identifies the gateway and its harness protocol clients.
package buildinfo

import (
	"fmt"
	"runtime"
	"runtime/debug"
)

const Version = "0.1.6"

const RepositoryURL = "https://github.com/Harness-Gateway/Harness-Gateway"

func String() string {
	return fmt.Sprintf("Harness-Gateway v%s (%s; %s/%s)", Version, Revision(), runtime.GOOS, runtime.GOARCH)
}

// Revision identifies the source checkout or the installed module version.
func Revision() string {
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
	return revision
}
