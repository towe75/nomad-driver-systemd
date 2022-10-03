package main

import (
	"github.com/coreos/go-systemd/v22/dbus"
	ds "github.com/godbus/dbus/v5"
)

type execStopPost struct {
	Path             string   // the binary path to execute
	Args             []string // an array with all arguments to pass to the executed command, starting with argument 0
	UncleanIsFailure bool     // a boolean whether it should be considered a failure if the process exits uncleanly
}

type envEntry struct {
	Key   string
	Value string
}

func PropExecStopPost(command []string) dbus.Property {
	return dbus.Property{
		Name: "ExecStopPost",
		Value: ds.MakeVariant([]execStopPost{
			{
				Path:             command[0],
				Args:             command,
				UncleanIsFailure: true,
			},
		}),
	}
}

func PropEnvironment(env map[string]string) dbus.Property {
	entries := []string{}
	for k, v := range env {
		// FIXME proper escaping
		entries = append(entries, k+"="+v)
	}
	return dbus.Property{
		Name:  "Environment",
		Value: ds.MakeVariant(entries),
	}
}

func PropString(name string, value string) dbus.Property {
	return dbus.Property{
		Name:  name,
		Value: ds.MakeVariant(value),
	}
}

func PropBool(name string, value bool) dbus.Property {
	return dbus.Property{
		Name:  name,
		Value: ds.MakeVariant(value),
	}
}
