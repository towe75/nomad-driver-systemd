package main

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"sync"
	"time"

	"github.com/coreos/go-systemd/v22/dbus"
	"github.com/hashicorp/go-hclog"
	"github.com/hashicorp/nomad/client/stats"
	"github.com/hashicorp/nomad/drivers/shared/eventer"
	"github.com/hashicorp/nomad/plugins/base"
	"github.com/hashicorp/nomad/plugins/drivers"
	"github.com/hashicorp/nomad/plugins/shared/hclspec"
	"github.com/hashicorp/nomad/plugins/shared/structs"
)

const (
	// pluginName is the name of the plugin
	// this is used for logging and (along with the version) for uniquely
	// identifying plugin binaries fingerprinted by the client
	pluginName = "systemd"

	// pluginVersion allows the client to identify and use newer versions of
	// an installed plugin
	pluginVersion = "v0.1.0"

	// fingerprintPeriod is the interval at which the plugin will send
	// fingerprint responses
	fingerprintPeriod = 30 * time.Second

	// taskHandleVersion is the version of task handle which this plugin sets
	// and understands how to decode
	// this is used to allow modification and migration of the task schema
	// used by the plugin
	taskHandleVersion = 1
)

var (
	unitNameRegex = regexp.MustCompile(`nomad_.*_.*\.service`)
)

// TaskState is the runtime state which is encoded in the handle returned to
// Nomad client.
// This information is needed to rebuild the task state and handler during
// recovery.
type TaskState struct {
	TaskConfig *drivers.TaskConfig
	StartedAt  time.Time

	// The plugin keeps track of its running tasks in a in-memory data
	// structure. If the plugin crashes, this data will be lost, so Nomad
	// will respawn a new instance of the plugin and try to restore its
	// in-memory representation of the running tasks using the RecoverTask()
	// method below.
	UnitName string
}

// Driver is an example driver plugin. When provisioned in a job,
// the taks will output a greet specified by the user.
type Driver struct {
	// eventer is used to handle multiplexing of TaskEvents calls such that an
	// event can be broadcast to all callers
	eventer *eventer.Eventer

	// config is the plugin configuration set by the SetConfig RPC
	config *PluginConfig

	// nomadConfig is the client config from Nomad
	nomadConfig *base.ClientDriverConfig

	// tasks is the in memory datastore mapping taskIDs to driver handles
	tasks *taskStore

	// ctx is the context for the driver. It is passed to other subsystems to
	// coordinate shutdown
	ctx context.Context

	// signalShutdown is called when the driver is shutting down and cancels
	// the ctx passed to any subsystems
	signalShutdown context.CancelFunc

	// logger will log to the Nomad agent
	logger hclog.Logger

	// our dbus connection
	busCon *dbus.Conn
	// guards connection access
	cmutex sync.Mutex
}

// NewSystemdDriver returns a new DriverPlugin implementation
func NewSystemdDriver(logger hclog.Logger) drivers.DriverPlugin {
	ctx, cancel := context.WithCancel(context.Background())
	logger = logger.Named(pluginName)

	return &Driver{
		eventer:        eventer.NewEventer(ctx, logger),
		config:         &PluginConfig{},
		tasks:          newTaskStore(),
		ctx:            ctx,
		signalShutdown: cancel,
		logger:         logger,
	}
}

// PluginInfo returns information describing the plugin.
func (d *Driver) PluginInfo() (*base.PluginInfoResponse, error) {
	return pluginInfo, nil
}

// ConfigSchema returns the plugin configuration schema.
func (d *Driver) ConfigSchema() (*hclspec.Spec, error) {
	return configSpec, nil
}

// SetConfig is called by the client to pass the configuration for the plugin.
func (d *Driver) SetConfig(cfg *base.Config) error {
	var config PluginConfig
	if len(cfg.PluginConfig) != 0 {
		if err := base.MsgPackDecode(cfg.PluginConfig, &config); err != nil {
			return err
		}
	}

	// Save the configuration to the plugin
	d.config = &config

	// TODO: parse and validated any configuration value if necessary.
	//
	// If your driver agent configuration requires any complex validation
	// (some dependency between attributes) or special data parsing (the
	// string "10s" into a time.Interval) you can do it here and update the
	// value in d.config.
	//
	// In the example below we check if the shell specified by the user is
	// supported by the plugin.
	// shell := d.config.Shell
	// if shell != "bash" && shell != "fish" {
	// 	return fmt.Errorf("invalid shell %s", d.config.Shell)
	// }

	// Save the Nomad agent configuration
	if cfg.AgentConfig != nil {
		d.nomadConfig = cfg.AgentConfig.Driver
	}

	// TODO: initialize any extra requirements if necessary.
	//
	// Here you can use the config values to initialize any resources that are
	// shared by all tasks that use this driver, such as a daemon process.

	return nil
}

// TaskConfigSchema returns the HCL schema for the configuration of a task.
func (d *Driver) TaskConfigSchema() (*hclspec.Spec, error) {
	return taskConfigSpec, nil
}

// Capabilities returns the features supported by the driver.
func (d *Driver) Capabilities() (*drivers.Capabilities, error) {
	return capabilities, nil
}

// Fingerprint returns a channel that will be used to send health information
// and other driver specific node attributes.
func (d *Driver) Fingerprint(ctx context.Context) (<-chan *drivers.Fingerprint, error) {
	ch := make(chan *drivers.Fingerprint)
	go d.handleFingerprint(ctx, ch)
	return ch, nil
}

func (d *Driver) connection() (*dbus.Conn, error) {
	d.cmutex.Lock()
	defer d.cmutex.Unlock()

	if d.busCon != nil {
		return d.busCon, nil
	}

	newconn, err := dbus.NewWithContext(d.ctx)
	if err != nil {
		return nil, err
	}

	d.busCon = newconn
	d.logger.Debug("Dbus connected")
	return d.busCon, nil
}

// handleFingerprint manages the channel and the flow of fingerprint data.
func (d *Driver) handleFingerprint(ctx context.Context, ch chan<- *drivers.Fingerprint) {
	defer close(ch)

	// Nomad expects the initial fingerprint to be sent immediately
	ticker := time.NewTimer(0)
	for {
		select {
		case <-ctx.Done():
			return
		case <-d.ctx.Done():
			return
		case <-ticker.C:
			// after the initial fingerprint we can set the proper fingerprint
			// period
			ticker.Reset(fingerprintPeriod)
			ch <- d.buildFingerprint()
		}
	}
}

// buildFingerprint returns the driver's fingerprint data
func (d *Driver) buildFingerprint() *drivers.Fingerprint {

	fp := &drivers.Fingerprint{
		Attributes:        map[string]*structs.Attribute{},
		Health:            drivers.HealthStateHealthy,
		HealthDescription: drivers.DriverHealthy,
	}

	conn, err := d.connection()
	if err != nil {
		d.logger.Error("No dbus connection", "err", err)
		// we're unhealthy
		return &drivers.Fingerprint{
			Health:            drivers.HealthStateUndetected,
			HealthDescription: fmt.Sprintf("No dbus connection"),
		}
	}

	d.cmutex.Lock()
	defer d.cmutex.Unlock()

	// check if we can talk to systemd, get the system state
	_, err = conn.SystemStateContext(d.ctx)
	if err != nil {
		d.logger.Error("Unable to get system state", "err", err)
		d.busCon.Close()
		d.busCon = nil
		// we're unhealthy
		return &drivers.Fingerprint{
			Health:            drivers.HealthStateUndetected,
			HealthDescription: fmt.Sprintf("No dbus connection"),
		}
	}

	// check if system state is (still) "running"
	// if prop.Value.Value().(string) != "running" {
	// 	d.logger.Error("System state incorrect", "state", prop.Value.Value().(string))
	// 	d.busCon.Close()
	// 	d.busCon = nil
	// 	// we're unhealthy
	// 	return &drivers.Fingerprint{
	// 		Health:            drivers.HealthStateUndetected,
	// 		HealthDescription: fmt.Sprintf("System state incorrect: %v", prop.Value.Value()),
	// 	}
	// }

	// ready to rock
	return fp
}

// StartTask returns a task handle and a driver network if necessary.
func (d *Driver) StartTask(cfg *drivers.TaskConfig) (*drivers.TaskHandle, *drivers.DriverNetwork, error) {
	if _, ok := d.tasks.Get(cfg.ID); ok {
		return nil, nil, fmt.Errorf("task with ID %q already started", cfg.ID)
	}

	var driverConfig TaskConfig
	if err := cfg.DecodeDriverConfig(&driverConfig); err != nil {
		return nil, nil, fmt.Errorf("failed to decode driver config: %v", err)
	}

	d.logger.Trace("starting task", "driver_cfg", hclog.Fmt("%+v", driverConfig))
	handle := drivers.NewTaskHandle(taskHandleVersion)
	handle.Config = cfg

	if driverConfig.Command == "" {
		return nil, nil, fmt.Errorf("command name required")
	}
	// autoprefix command name with local task dir in case that it is not
	// a absolute path
	if driverConfig.Command[0] != '/' {
		driverConfig.Command = cfg.TaskDir().Dir + "/local/" + driverConfig.Command
	}

	// TODO ensure to include port_map into tasks environment map
	//cfg.Env = taskenv.SetPortMapEnvs(cfg.Env, driverConfig.PortMap)

	execStart := []string{driverConfig.Command}
	execStart = append(execStart, driverConfig.Args...)
	// populate properties for the to-be-started systemd transient unit
	properties := []dbus.Property{
		// set ID as Description. Helps with relating events back to task
		dbus.PropDescription(cfg.ID),
		dbus.PropType("simple"),

		// do not remove service unit when process dies
		// this allows us to get the terminal state unit properties back into the plugin
		PropBool("RemainAfterExit", true),

		// to-be-started command incl. args
		dbus.PropExecStart(execStart, true),

		// set proper task environment
		PropEnvironment(cfg.Env),
		// transient units are gone when main process finishes
		// we need to write a file here to get exit code back into nomad
		//PropExecStopPost([]string{"env"}),
		PropString("User", cfg.User),

		// Enforce memory limits
		PropBool("MemoryAccounting", true),
		// todo: configurable swap
		PropUInt64("MemorySwapMax", uint64(0)),
	}

	// Memory
	// FIXME: handle MemoryMB vs MemoryMaxMB, swap etc
	if cfg.Resources.NomadResources.Memory.MemoryMaxMB > 0 {
		properties = append(properties, PropUInt64("MemoryMax", uint64(cfg.Resources.NomadResources.Memory.MemoryMaxMB*1024*1024)))
	}

	if cfg.Resources.NomadResources.Memory.MemoryMB > 0 {
		if cfg.Resources.NomadResources.Memory.MemoryMaxMB > 0 {
			properties = append(properties, PropUInt64("MemoryHigh", uint64(cfg.Resources.NomadResources.Memory.MemoryMB*1024*1024)))
		} else {
			properties = append(properties, PropUInt64("MemoryMax", uint64(cfg.Resources.NomadResources.Memory.MemoryMB*1024*1024)))
		}
	}

	// Logging
	if driverConfig.Logging.Driver == "" || driverConfig.Logging.Driver == LOG_DRIVER_NOMAD {
		properties = append(properties, PropString("StandardOutputFileToAppend", cfg.StdoutPath))
		properties = append(properties, PropString("StandardErrorFileToAppend", cfg.StderrPath))
	} else if driverConfig.Logging.Driver == LOG_DRIVER_JOURNALD {
		properties = append(properties, PropString("StandardOutput", "journal"))
		properties = append(properties, PropString("StandardError", "journal"))
		// TODO: configure LogExtraFields
	} else {
		return nil, nil, fmt.Errorf("Invalid logging.driver option")
	}

	//hard, soft, err := memoryLimits(cfg.Resources.NomadResources.Memory, driverConfig.MemoryReservation)

	unitName := BuildUnitNameForTask(cfg)
	startupCh := make(chan string)
	conn, err := d.connection()
	if err != nil {
		return nil, nil, fmt.Errorf("failed to start task, no dbus connection: %v", err)
	}

	res, err := conn.StartTransientUnitContext(d.ctx, unitName, "replace", properties, startupCh)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to start task, could not create unit: %v", err)
	}

	// wait for startup
	<-startupCh

	h := &taskHandle{
		taskConfig:         cfg,
		totalCPUStats:      stats.NewCpuStats(),
		procState:          drivers.TaskStateRunning,
		startedAt:          time.Now().Round(time.Millisecond),
		logger:             d.logger,
		unitName:           unitName,
		driver:             d,
		collectionInterval: time.Second,
		exitResult:         &drivers.ExitResult{},
	}

	driverState := TaskState{
		TaskConfig: cfg,
		StartedAt:  h.startedAt,
		UnitName:   h.unitName,
	}

	if err := handle.SetDriverState(&driverState); err != nil {
		return nil, nil, fmt.Errorf("failed to set driver state: %v", err)
	}

	d.tasks.Set(cfg.ID, h)

	go h.runUnitMonitor()
	d.logger.Info("Started unit", "unit", unitName, "id", res)
	return handle, nil, nil
}

// BuildUnitNameForTask returns the systemd unit name for a specific Task in our group
func BuildUnitNameForTask(cfg *drivers.TaskConfig) string {
	return fmt.Sprintf("nomad_%s_%s.service", cfg.Name, cfg.AllocID)
}

// RecoverTask recreates the in-memory state of a task from a TaskHandle.
func (d *Driver) RecoverTask(handle *drivers.TaskHandle) error {
	if handle == nil {
		return errors.New("error: handle cannot be nil")
	}

	if _, ok := d.tasks.Get(handle.Config.ID); ok {
		return nil
	}

	var taskState TaskState
	if err := handle.GetDriverState(&taskState); err != nil {
		return fmt.Errorf("failed to decode task state from handle: %v", err)
	}

	d.logger.Debug("Checking for recoverable task", "task", handle.Config.Name, "taskid", handle.Config.ID, "unit", taskState.UnitName)

	conn, err := d.connection()
	if err != nil {
		return fmt.Errorf("failed to recover task, no dbus connection: %v", err)
	}

	state, err := conn.GetUnitPropertyContext(d.ctx, taskState.UnitName, "ActiveState")
	if err == nil {
		if state.Value.String() == "inactive" {
			d.logger.Debug("Found a inactive unit", "task", handle.Config.ID, "unit", taskState.UnitName)
			return nil
		}
	} else {
		d.logger.Debug("Recovery lookup found no unit", "task", handle.Config.ID, "unit", taskState.UnitName, "error", err)
		return nil
	}

	h := &taskHandle{
		procState:     drivers.TaskStateRunning,
		totalCPUStats: stats.NewCpuStats(),

		taskConfig: taskState.TaskConfig,
		startedAt:  taskState.StartedAt,
		unitName:   taskState.UnitName,

		exitResult:         &drivers.ExitResult{},
		logger:             d.logger,
		driver:             d,
		collectionInterval: time.Second,
	}

	d.tasks.Set(taskState.TaskConfig.ID, h)

	go h.runUnitMonitor()

	d.logger.Debug("Recovered task unit", "task", handle.Config.ID, "unit", taskState.UnitName)
	return nil
}

// WaitTask returns a channel used to notify Nomad when a task exits.
func (d *Driver) WaitTask(ctx context.Context, taskID string) (<-chan *drivers.ExitResult, error) {
	d.logger.Debug("WaitTask called", "task", taskID)
	handle, ok := d.tasks.Get(taskID)
	if !ok {
		return nil, drivers.ErrTaskNotFound
	}
	ch := make(chan *drivers.ExitResult)

	go handle.runExitWatcher(ctx, ch)
	return ch, nil
}

func (d *Driver) handleWait(ctx context.Context, handle *taskHandle, ch chan *drivers.ExitResult) {
	defer close(ch)

	// TODO: implement driver specific logic to notify Nomad the task has been
	// var result *drivers.ExitResult
	// result = &drivers.ExitResult{
	// 	ExitCode: 0,
	// 	Signal:   0,
	// }

	// completed and what was the exit result.
	//
	// When a result is sent in the result channel Nomad will stop the task and
	// emit an event that an operator can use to get an insight on why the task
	// stopped.
	//
	// In the example below we block and wait until the executor finishes
	// running, at which point we send the exit code and signal in the result
	// channel.
	// ps, err := handle.exec.Wait(ctx)
	// if err != nil {
	// 	result = &drivers.ExitResult{
	// 		Err: fmt.Errorf("executor: error waiting on process: %v", err),
	// 	}
	// } else {
	// 	result = &drivers.ExitResult{
	// 		ExitCode: ps.ExitCode,
	// 		Signal:   ps.Signal,
	// 	}
	// }

	for {
		select {
		case <-ctx.Done():
			return
		case <-d.ctx.Done():
			return
			//case ch <- result:
		}
	}
}

// StopTask stops a running task with the given signal and within the timeout window.
func (d *Driver) StopTask(taskID string, timeout time.Duration, signal string) error {
	d.logger.Info("Stopping task", "taskID", taskID, "signal", signal)
	handle, ok := d.tasks.Get(taskID)
	if !ok {
		return drivers.ErrTaskNotFound
	}

	ctx, cancel := context.WithTimeout(d.ctx, timeout)
	defer cancel()

	conn, err := d.connection()
	if err != nil {
		return fmt.Errorf("failed to stop task, no dbus connection: %v", err)
	}
	_, err = conn.StopUnitContext(ctx, handle.unitName, "replace", nil)
	if err != nil {
		d.logger.Error("Could not stop/kill unit", "unit", handle.unitName, "error", err)
		return err
	}

	return nil
}

// DestroyTask cleans up and removes a task that has terminated.
func (d *Driver) DestroyTask(taskID string, force bool) error {
	d.logger.Info("Destroy task", "taskID", taskID)
	handle, ok := d.tasks.Get(taskID)
	if !ok {
		return drivers.ErrTaskNotFound
	}

	if handle.IsRunning() && !force {
		return errors.New("cannot destroy running task")
	}

	if handle.isRunning() {
		d.logger.Debug("Have to destroyTask but unit is still running", "unit", handle.unitName)
		// we can not do anything, so catching the error is useless

		conn, err := d.connection()
		if err != nil {
			return fmt.Errorf("failed to stop task, no dbus connection: %v", err)
		}
		_, err = conn.StopUnitContext(d.ctx, handle.unitName, "replace", nil)
		if err != nil {
			d.logger.Error("Could not stop/kill unit", "unit", handle.unitName, "error", err)
			return err
		}
		// wait a while for stats emitter to collect exit code etc.
		for i := 0; i < 20; i++ {
			if !handle.isRunning() {
				break
			}
			time.Sleep(time.Millisecond * 250)
		}
		if handle.isRunning() {
			d.logger.Warn("stats emitter did not exit while stop/kill unit during destroy", "error", err)
		}
	}

	d.tasks.Delete(taskID)

	return nil
}

// InspectTask returns detailed status information for the referenced taskID.
func (d *Driver) InspectTask(taskID string) (*drivers.TaskStatus, error) {
	handle, ok := d.tasks.Get(taskID)
	if !ok {
		return nil, drivers.ErrTaskNotFound
	}

	return handle.TaskStatus(), nil
}

// TaskStats returns a channel which the driver should send stats to at the given interval.
func (d *Driver) TaskStats(ctx context.Context, taskID string, interval time.Duration) (<-chan *drivers.TaskResourceUsage, error) {
	handle, ok := d.tasks.Get(taskID)
	if !ok {
		return nil, drivers.ErrTaskNotFound
	}

	statsChannel := make(chan *drivers.TaskResourceUsage)
	go handle.runStatsEmitter(ctx, statsChannel, interval)
	return statsChannel, nil
}

// TaskEvents returns a channel that the plugin can use to emit task related events.
func (d *Driver) TaskEvents(ctx context.Context) (<-chan *drivers.TaskEvent, error) {
	return d.eventer.TaskEvents(ctx)
}

// SignalTask forwards a signal to a task.
// This is an optional capability.
func (d *Driver) SignalTask(taskID string, signal string) error {
	_, ok := d.tasks.Get(taskID)
	if !ok {
		return drivers.ErrTaskNotFound
	}

	// TODO: implement driver specific signal handling logic.
	//
	// The given signal must be forwarded to the target taskID. If this plugin
	// doesn't support receiving signals (capability SendSignals is set to
	// false) you can just return nil.
	// sig := os.Interrupt
	// if s, ok := signals.SignalLookup[signal]; ok {
	// 	sig = s
	// } else {
	// 	d.logger.Warn("unknown signal to send to task, using SIGINT instead", "signal", signal, "task_id", handle.taskConfig.ID)

	// }

	// FIXME: implement
	return drivers.ErrTaskNotFound
	//return handle.exec.Signal(sig)
}

// ExecTask returns the result of executing the given command inside a task.
// This is an optional capability.
func (d *Driver) ExecTask(taskID string, cmd []string, timeout time.Duration) (*drivers.ExecTaskResult, error) {
	// TODO: implement driver specific logic to execute commands in a task.
	return nil, errors.New("This driver does not support exec")
}
