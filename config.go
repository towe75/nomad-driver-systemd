package main

import (
	"github.com/hashicorp/nomad/plugins/base"
	"github.com/hashicorp/nomad/plugins/drivers"
	"github.com/hashicorp/nomad/plugins/shared/hclspec"
)

const (
	LOG_DRIVER_NOMAD    = "nomad"
	LOG_DRIVER_JOURNALD = "journald"
)

var (
	// pluginInfo describes the plugin
	pluginInfo = &base.PluginInfoResponse{
		Type:              base.PluginTypeDriver,
		PluginApiVersions: []string{drivers.ApiVersion010},
		PluginVersion:     pluginVersion,
		Name:              pluginName,
	}

	// configSpec is the specification of the plugin's configuration
	// this is used to validate the configuration specified for the plugin
	// on the client.
	// this is not global, but can be specified on a per-client basis.
	configSpec = hclspec.NewObject(map[string]*hclspec.Spec{
		// TODO: define plugin's agent configuration schema.
		//
		// The schema should be defined using HCL specs and it will be used to
		// validate the agent configuration provided by the user in the
		// `plugin` stanza (https://www.nomadproject.io/docs/configuration/plugin.html).
		//
		// For example, for the schema below a valid configuration would be:
		//
		//   plugin "hello-driver-plugin" {
		//     config {
		//       shell = "fish"
		//     }
		//   }
		// "shell": hclspec.NewDefault(
		// 	hclspec.NewAttr("shell", "string", false),
		// 	hclspec.NewLiteral(`"bash"`),
		// ),
	})

	// taskConfigSpec is the specification of the plugin's configuration for
	// a task
	// this is used to validated the configuration specified for the plugin
	// when a job is submitted.
	taskConfigSpec = hclspec.NewObject(map[string]*hclspec.Spec{
		// TODO: define plugin's task configuration schema
		//
		// The schema should be defined using HCL specs and it will be used to
		// validate the task configuration provided by the user when they
		// submit a job.
		//
		// For example, for the schema below a valid task would be:
		//   job "example" {
		//     group "example" {
		//       task "say-hi" {
		//         driver = "hello-driver-plugin"
		//         config {
		//           greeting = "Hi"
		//         }
		//       }
		//     }
		//   }
		"command": hclspec.NewAttr("command", "string", false),
		"args":    hclspec.NewAttr("args", "list(string)", false),
		"logging": hclspec.NewBlock("logging", false, hclspec.NewObject(map[string]*hclspec.Spec{
			"driver": hclspec.NewAttr("driver", "string", false),
			//"extraFields": hclspec.NewAttr("options", "map(string)", false),
		})),
	})

	// capabilities indicates what optional features this driver supports
	// this should be set according to the target run time.
	capabilities = &drivers.Capabilities{
		// TODO: set plugin's capabilities
		//
		// The plugin's capabilities signal Nomad which extra functionalities
		// are supported. For a list of available options check the docs page:
		// https://godoc.org/github.com/hashicorp/nomad/plugins/drivers#Capabilities
		SendSignals: true,
		Exec:        false,
	}
)

// PluginConfig contains configuration information for the plugin
type PluginConfig struct {
	// TODO: create decoded plugin configuration struct
	//
	// This struct is the decoded version of the schema defined in the
	// configSpec variable above. It's used to convert the HCL configuration
	// passed by the Nomad agent into Go contructs.
	// Shell string `codec:"shell"`
}

// TaskConfig contains configuration information for a task that runs with
// this plugin
type TaskConfig struct {
	Command string        `codec:"command"`
	Args    []string      `codec:"args"`
	Logging LoggingConfig `codec:"logging"`
}

// LoggingConfig is the tasks logging configuration
type LoggingConfig struct {
	Driver string `codec:"driver"`
	//ExtraFields hclutils.MapStrStr `codec:"extraFields"`
}
