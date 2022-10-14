package main

import (
	"context"
	"fmt"
	"io/ioutil"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/hashicorp/go-hclog"
	"github.com/hashicorp/nomad/client/taskenv"
	"github.com/hashicorp/nomad/helper/testlog"
	"github.com/hashicorp/nomad/helper/uuid"
	"github.com/hashicorp/nomad/nomad/structs"
	"github.com/hashicorp/nomad/plugins/base"
	"github.com/hashicorp/nomad/plugins/drivers"
	dtestutil "github.com/hashicorp/nomad/plugins/drivers/testutils"
	tu "github.com/hashicorp/nomad/testutil"
	"github.com/stretchr/testify/require"
)

var (
	longRunningCommand = []string{"sleep", "1000d"}
)

// systemdDriverHarness wires up everything needed to launch a task with a systed driver.
// A driver plugin interface and cleanup function is returned
func systemdDriverHarness(t *testing.T, cfg map[string]interface{}) *dtestutil.DriverHarness {

	logger := testlog.HCLogger(t)
	if testing.Verbose() {
		logger.SetLevel(hclog.Trace)
	} else {
		logger.SetLevel(hclog.Info)
	}

	baseConfig := base.Config{}
	pluginConfig := PluginConfig{}
	if err := base.MsgPackEncode(&baseConfig.PluginConfig, &pluginConfig); err != nil {
		t.Error("Unable to encode plugin config", err)
	}

	d := NewSystemdDriver(logger).(*Driver)
	d.SetConfig(&baseConfig)
	d.buildFingerprint()
	// d.config.Volumes.Enabled = true
	// if enforce, err := ioutil.ReadFile("/sys/fs/selinux/enforce"); err == nil {
	// 	if string(enforce) == "1" {
	// 		d.logger.Info("Enabling SelinuxLabel")
	// 		d.config.Volumes.SelinuxLabel = "z"
	// 	}
	// }
	// d.config.GC.Container = true
	// if v, ok := cfg["GC.Container"]; ok {
	// 	if bv, ok := v.(bool); ok {
	// 		d.config.GC.Container = bv
	// 	}
	// }

	harness := dtestutil.NewDriverHarness(t, d)

	return harness
}

func createBasicResources() *drivers.Resources {
	res := drivers.Resources{
		NomadResources: &structs.AllocatedTaskResources{
			Memory: structs.AllocatedMemoryResources{
				MemoryMB: 100,
			},
			Cpu: structs.AllocatedCpuResources{
				CpuShares: 250,
			},
		},
		LinuxResources: &drivers.LinuxResources{
			CPUPeriod:        100000,
			CPUQuota:         100000,
			CPUShares:        500,
			MemoryLimitBytes: 256 * 1024 * 1024,
			PercentTicks:     float64(500) / float64(2000),
		},
	}
	return &res
}

func getSystemdDriver(t *testing.T, harness *dtestutil.DriverHarness) *Driver {
	driver, ok := harness.Impl().(*Driver)
	require.True(t, ok)
	return driver
}

func newTaskConfig(command []string) TaskConfig {

	return TaskConfig{
		Command: command[0],
		Args:    command[1:],
	}
}

// read a tasks stdout logfile into a string, fail on error
func readStdoutLog(t *testing.T, task *drivers.TaskConfig) string {
	logfile := filepath.Join(filepath.Dir(task.StdoutPath), fmt.Sprintf("%s.stdout.0", task.Name))
	stdout, err := ioutil.ReadFile(logfile)
	require.NoError(t, err)
	return string(stdout)
}

// read a tasks stderr logfile into a string, fail on error
func readStderrLog(t *testing.T, task *drivers.TaskConfig) string {
	logfile := filepath.Join(filepath.Dir(task.StderrPath), fmt.Sprintf("%s.stderr.0", task.Name))
	stderr, err := ioutil.ReadFile(logfile)
	require.NoError(t, err)
	return string(stderr)
}

// can we at least setup the harness?
func TestSystemdDriver_Setup(t *testing.T) {
	systemdDriverHarness(t, nil)
}

// Ensure that we require a command
func TestSystemdDriver_Start_NoCommand(t *testing.T) {
	if !tu.IsCI() {
		t.Parallel()
	}

	taskCfg := TaskConfig{}
	task := &drivers.TaskConfig{
		ID:        uuid.Generate(),
		Name:      "nocommand",
		AllocID:   uuid.Generate(),
		Resources: createBasicResources(),
	}
	require.NoError(t, task.EncodeConcreteDriverConfig(&taskCfg))

	d := systemdDriverHarness(t, nil)
	cleanup := d.MkAllocDir(task, false)
	defer cleanup()

	_, _, err := d.StartTask(task)
	require.Error(t, err)
	require.Contains(t, err.Error(), "command name required")

	d.DestroyTask(task.ID, true)

}

// start a long running command
func TestSystemdDriver_LongRunning(t *testing.T) {
	if !tu.IsCI() {
		t.Parallel()
	}

	taskCfg := newTaskConfig(longRunningCommand)
	task := &drivers.TaskConfig{
		ID:        uuid.Generate(),
		Name:      "longrunning",
		AllocID:   uuid.Generate(),
		Resources: createBasicResources(),
	}
	require.NoError(t, task.EncodeConcreteDriverConfig(&taskCfg))

	d := systemdDriverHarness(t, nil)
	cleanup := d.MkAllocDir(task, true)
	defer cleanup()

	_, _, err := d.StartTask(task)
	require.NoError(t, err)

	defer d.DestroyTask(task.ID, true)

	// Attempt to wait
	waitCh, err := d.WaitTask(context.Background(), task.ID)
	require.NoError(t, err)

	select {
	case <-waitCh:
		t.Fatalf("wait channel should not have received an exit result")
	case <-time.After(time.Duration(tu.TestMultiplier()*3) * time.Second):
	}
}

// test a short-living command
func TestSystemdDriver_ShortLiving(t *testing.T) {
	if !tu.IsCI() {
		t.Parallel()
	}

	taskCfg := newTaskConfig([]string{"echo"})
	task := &drivers.TaskConfig{
		ID:        uuid.Generate(),
		Name:      "shortliving",
		AllocID:   uuid.Generate(),
		Resources: createBasicResources(),
	}
	require.NoError(t, task.EncodeConcreteDriverConfig(&taskCfg))

	d := systemdDriverHarness(t, nil)
	cleanup := d.MkAllocDir(task, true)
	defer cleanup()

	_, _, err := d.StartTask(task)
	require.NoError(t, err)

	defer d.DestroyTask(task.ID, true)

	// Attempt to wait
	waitCh, err := d.WaitTask(context.Background(), task.ID)
	require.NoError(t, err)

	select {
	case res := <-waitCh:
		if !res.Successful() {
			require.Fail(t, "ExitResult should be successful: %v", res)
		}
	case <-time.After(time.Duration(tu.TestMultiplier()*5) * time.Second):
		require.Fail(t, "timeout")
	}
}

func TestSystemdDriver_AllocDir(t *testing.T) {
	if !tu.IsCI() {
		t.Parallel()
	}

	exp := []byte{'w', 'i', 'n'}
	file := "output.txt"

	taskCfg := newTaskConfig([]string{
		"sh",
		"-c",
		fmt.Sprintf(`echo -n %s > $%s/%s`,
			string(exp), taskenv.AllocDir, file),
	})
	task := &drivers.TaskConfig{
		ID:        uuid.Generate(),
		Name:      "allocDir",
		AllocID:   uuid.Generate(),
		Resources: createBasicResources(),
	}
	require.NoError(t, task.EncodeConcreteDriverConfig(&taskCfg))

	d := systemdDriverHarness(t, nil)
	cleanup := d.MkAllocDir(task, true)
	defer cleanup()

	_, _, err := d.StartTask(task)
	require.NoError(t, err)

	defer d.DestroyTask(task.ID, true)

	// Attempt to wait
	waitCh, err := d.WaitTask(context.Background(), task.ID)
	require.NoError(t, err)

	select {
	case res := <-waitCh:
		if !res.Successful() {
			require.Fail(t, fmt.Sprintf("ExitResult should be successful: %v", res))
		}
	case <-time.After(time.Duration(tu.TestMultiplier()*5) * time.Second):
		require.Fail(t, "timeout")
	}

	// Check that data was written to the shared alloc directory.
	outputFile := filepath.Join(task.TaskDir().SharedAllocDir, file)
	act, err := ioutil.ReadFile(outputFile)
	if err != nil {
		t.Fatalf("Couldn't read expected output: %v", err)
	}

	if !reflect.DeepEqual(act, exp) {
		t.Fatalf("Command outputted %v; want %v", act, exp)
	}
}

// Check log_opt=nomad logger
func TestSystemdDriver_LogNomad(t *testing.T) {
	if !tu.IsCI() {
		t.Parallel()
	}

	stdoutMagic := uuid.Generate()
	stderrMagic := uuid.Generate()

	taskCfg := newTaskConfig([]string{
		"sh",
		"-c",
		fmt.Sprintf("echo %s; 1>&2 echo %s", stdoutMagic, stderrMagic),
	})
	taskCfg.Logging.Driver = "nomad"
	task := &drivers.TaskConfig{
		ID:        uuid.Generate(),
		Name:      "logNomad",
		AllocID:   uuid.Generate(),
		Resources: createBasicResources(),
	}
	require.NoError(t, task.EncodeConcreteDriverConfig(&taskCfg))

	d := systemdDriverHarness(t, nil)
	cleanup := d.MkAllocDir(task, true)
	defer cleanup()

	_, _, err := d.StartTask(task)
	require.NoError(t, err)

	defer d.DestroyTask(task.ID, true)

	// Attempt to wait
	waitCh, err := d.WaitTask(context.Background(), task.ID)
	require.NoError(t, err)

	select {
	case <-waitCh:
	case <-time.After(time.Duration(tu.TestMultiplier()*5) * time.Second):
		t.Fatalf("Container did not exit in time")
	}

	stdoutLog := readStdoutLog(t, task)
	require.Contains(t, stdoutLog, stdoutMagic, "stdoutMagic in stdout")
	stderrLog := readStderrLog(t, task)
	require.Contains(t, stderrLog, stderrMagic, "stderrMagic in stderr")
}

// check setting a environment variable for the task
func TestSystemdDriver_Env(t *testing.T) {
	if !tu.IsCI() {
		t.Parallel()
	}

	taskCfg := newTaskConfig([]string{
		"echo",
		"$testvar",
	})

	testvalue := uuid.Generate()

	task := &drivers.TaskConfig{
		ID:        uuid.Generate(),
		Name:      "env",
		AllocID:   uuid.Generate(),
		Resources: createBasicResources(),
		Env:       map[string]string{"testvar": testvalue},
	}
	require.NoError(t, task.EncodeConcreteDriverConfig(&taskCfg))

	d := systemdDriverHarness(t, nil)
	cleanup := d.MkAllocDir(task, true)
	defer cleanup()

	_, _, err := d.StartTask(task)
	require.NoError(t, err)

	defer d.DestroyTask(task.ID, true)

	// Attempt to wait
	waitCh, err := d.WaitTask(context.Background(), task.ID)
	require.NoError(t, err)

	select {
	case res := <-waitCh:
		// should have a exitcode=0 result
		require.True(t, res.Successful())
	case <-time.After(time.Duration(tu.TestMultiplier()*5) * time.Second):
		t.Fatalf("Task did not exit in time")
	}

	// see if stdout was populated with the contents of testvalue
	tasklog := readStdoutLog(t, task)
	require.Contains(t, tasklog, testvalue)

}

// check setting a user for the task
func TestSystemdDriver_User(t *testing.T) {
	if !tu.IsCI() {
		t.Parallel()
	}

	taskCfg := newTaskConfig([]string{
		// print our username to stdout
		"whoami",
	})

	task := &drivers.TaskConfig{
		ID:        uuid.Generate(),
		Name:      "user",
		AllocID:   uuid.Generate(),
		Resources: createBasicResources(),
	}
	// use "www-data" as a user for our test, it's part of the busybox image
	task.User = "www-data"
	require.NoError(t, task.EncodeConcreteDriverConfig(&taskCfg))

	d := systemdDriverHarness(t, nil)
	cleanup := d.MkAllocDir(task, true)
	defer cleanup()

	_, _, err := d.StartTask(task)
	require.NoError(t, err)

	defer d.DestroyTask(task.ID, true)

	// Attempt to wait
	waitCh, err := d.WaitTask(context.Background(), task.ID)
	require.NoError(t, err)

	select {
	case res := <-waitCh:
		// should have a exitcode=0 result
		require.True(t, res.Successful())
	case <-time.After(time.Duration(tu.TestMultiplier()*5) * time.Second):
		t.Fatalf("Task did not exit in time")
	}

	// see if stdout was populated with the "whoami" output
	tasklog := readStdoutLog(t, task)
	require.Contains(t, tasklog, "www-data")

}

// test exit code propagation
func TestSystemdDriver_ExitCode(t *testing.T) {
	if !tu.IsCI() {
		t.Parallel()
	}

	taskCfg := newTaskConfig([]string{
		"sh",
		"-c",
		"exit 123",
	})
	task := &drivers.TaskConfig{
		ID:        uuid.Generate(),
		Name:      "exitcode",
		AllocID:   uuid.Generate(),
		Resources: createBasicResources(),
	}
	require.NoError(t, task.EncodeConcreteDriverConfig(&taskCfg))

	d := systemdDriverHarness(t, nil)
	cleanup := d.MkAllocDir(task, true)
	defer cleanup()

	_, _, err := d.StartTask(task)
	require.NoError(t, err)

	defer d.DestroyTask(task.ID, true)

	// Attempt to wait
	waitCh, err := d.WaitTask(context.Background(), task.ID)
	require.NoError(t, err)

	select {
	case res := <-waitCh:
		require.Equal(t, 123, res.ExitCode, "Should have got exit code 123")
	case <-time.After(time.Duration(tu.TestMultiplier()*5) * time.Second):
		t.Fatalf("Task did not exit in time")
	}
}

// test oom flag propagation
func TestSystemdDriver_OOM(t *testing.T) {

	if !tu.IsCI() {
		t.Parallel()
	}

	taskCfg := newTaskConfig([]string{
		// Incrementally creates a bigger and bigger variable.
		// "stress",
		// "--vm",
		// "1",
		"bash",
		"-ec",
		"sleep 1;for b in {0..99999999}; do a=$b$a; done",
	})

	task := &drivers.TaskConfig{
		ID:        uuid.Generate(),
		Name:      "oom",
		AllocID:   uuid.Generate(),
		Resources: createBasicResources(),
	}
	// limit memory to 5MB to trigger oom soon enough
	task.Resources.NomadResources.Memory.MemoryMB = 5
	require.NoError(t, task.EncodeConcreteDriverConfig(&taskCfg))

	d := systemdDriverHarness(t, nil)
	cleanup := d.MkAllocDir(task, true)
	defer cleanup()

	_, _, err := d.StartTask(task)
	require.NoError(t, err)

	defer d.DestroyTask(task.ID, true)

	// Attempt to wait
	waitCh, err := d.WaitTask(context.Background(), task.ID)
	require.NoError(t, err)

	select {
	case res := <-waitCh:
		require.False(t, res.Successful(), "Should have failed because of oom but was successful")
		require.True(t, res.OOMKilled, "OOM Flag not set")
	case <-time.After(time.Duration(tu.TestMultiplier()*5) * time.Second):
		t.Fatalf("Task did not exit in time")
	}
}
