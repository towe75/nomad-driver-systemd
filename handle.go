package main

import (
	"context"
	"sync"
	"time"

	"github.com/hashicorp/go-hclog"
	"github.com/hashicorp/nomad/client/stats"
	"github.com/hashicorp/nomad/plugins/drivers"
)

var (
	measuredCPUStats = []string{"System Mode", "User Mode", "Percent"}
	measuredMemStats = []string{"Usage", "Max Usage"}
)

// taskHandle should store all relevant runtime information
// such as process ID if this is a local task or other meta
// data if this driver deals with external APIs
type taskHandle struct {
	// stateLock syncs access to all fields below
	stateLock sync.RWMutex
	driver    *Driver

	logger             hclog.Logger
	taskConfig         *drivers.TaskConfig
	procState          drivers.TaskState
	startedAt          time.Time
	completedAt        time.Time
	exitResult         *drivers.ExitResult
	collectionInterval time.Duration

	// systemd unit name
	unitName string

	// task properties (periodically polled)
	properties map[string]interface{}

	totalCPUStats *stats.CpuStats
}

func (h *taskHandle) TaskStatus() *drivers.TaskStatus {
	h.stateLock.RLock()
	defer h.stateLock.RUnlock()

	return &drivers.TaskStatus{
		ID:               h.taskConfig.ID,
		Name:             h.taskConfig.Name,
		State:            h.procState,
		StartedAt:        h.startedAt,
		CompletedAt:      h.completedAt,
		ExitResult:       h.exitResult,
		DriverAttributes: map[string]string{
			//"pid": strconv.Itoa(h.pid),
		},
	}
}

func (h *taskHandle) IsRunning() bool {
	h.stateLock.RLock()
	defer h.stateLock.RUnlock()
	return h.procState == drivers.TaskStateRunning
}

func (h *taskHandle) runUnitMonitor() {
	h.logger.Debug("Monitoring Unit", "unit", h.unitName)
	timer := time.NewTimer(0)

	for {
		select {
		case <-h.driver.ctx.Done():
			return

		case <-timer.C:
			timer.Reset(h.collectionInterval)
		}

		gone := false
		conn, err := h.driver.connection()
		if err != nil {
			h.logger.Warn("Unable to get unit properties, no dbus connection", "unit", h.unitName, "err", err)
			gone = true
		} else {
			props, err := conn.GetAllPropertiesContext(h.driver.ctx, h.unitName)
			if err == nil {
				// cache latest known property set in handle. It's used by the stats collector
				h.stateLock.Lock()
				h.properties = props
				h.stateLock.Unlock()
				state := props["ActiveState"]
				// is the unit still running?
				if state == "inactive" || state == "failed" || props["SubState"] == "exited" {
					gone = true
					h.logger.Debug("props", "Result", props["Result"])
					if props["Result"] == "oom-kill" {
						// propagate OOM kills
						h.exitResult.OOMKilled = true
					}
					h.exitResult.ExitCode = int(props["ExecMainStatus"].(int32))
					// cleanup
					_, err = conn.StopUnitContext(h.driver.ctx, h.unitName, "replace", nil)
					if err != nil {
						// Might not be a problem. This can be the case when we run a regular StopTask operation
						h.logger.Debug("Unable to stop/remove completed unit", "unit", h.unitName, "err", err)
					} else {
						h.logger.Debug("Unit removed", "unit", h.unitName)

					}
				}
			} else {
				h.logger.Warn("Unable to get unit properties, may be gone", "unit", h.unitName, "err", err)
				gone = true
			}
		}

		if gone {
			h.logger.Info("Unit is not running anymore", "unit", h.unitName)
			// container was stopped, get exit code and other post mortem infos
			h.stateLock.Lock()
			h.completedAt = time.Now()

			h.procState = drivers.TaskStateExited
			h.stateLock.Unlock()
			return
		}

	}
}

func (h *taskHandle) isRunning() bool {
	h.stateLock.RLock()
	defer h.stateLock.RUnlock()
	return h.procState == drivers.TaskStateRunning
}

func (h *taskHandle) runExitWatcher(ctx context.Context, exitChannel chan *drivers.ExitResult) {
	timer := time.NewTimer(0)
	h.logger.Debug("Starting exitWatcher", "unit", h.unitName)

	defer func() {
		h.logger.Debug("Stopping exitWatcher", "unit", h.unitName)
		// be sure to get the whole result
		h.stateLock.Lock()
		result := h.exitResult
		h.stateLock.Unlock()
		exitChannel <- result
		close(exitChannel)
	}()

	for {
		if !h.isRunning() {
			return
		}
		select {
		case <-ctx.Done():
			return
		case <-timer.C:
			timer.Reset(time.Second)
		}
	}
}

func (h *taskHandle) runStatsEmitter(ctx context.Context, statsChannel chan *drivers.TaskResourceUsage, interval time.Duration) {
	timer := time.NewTimer(0)
	h.logger.Debug("Starting statsEmitter", "unit", h.unitName)
	h.collectionInterval = interval
	for {
		select {
		case <-ctx.Done():
			h.logger.Debug("Stopping statsEmitter", "unit", h.unitName)
			return
		case <-timer.C:
			timer.Reset(interval)
		}

		h.stateLock.Lock()
		t := time.Now()

		cpuNanosProp := h.properties["CPUUsageNSec"]
		memoryProp := h.properties["MemoryCurrent"]
		if cpuNanosProp == nil || memoryProp == nil {
			continue
		}

		cpuNanos := h.properties["CPUUsageNSec"].(uint64)
		memory := h.properties["MemoryCurrent"].(uint64)

		//FIXME implement cpu stats correctly
		totalPercent := h.totalCPUStats.Percent(float64(cpuNanos))
		cs := &drivers.CpuStats{
			SystemMode: 0, //h.systemCPUStats.Percent(float64(h.containerStats.CPUStats.CPUUsage.UsageInKernelmode)),
			UserMode:   totalPercent,
			Percent:    totalPercent,
			TotalTicks: h.totalCPUStats.TicksConsumed(totalPercent),
			Measured:   measuredCPUStats,
		}

		ms := &drivers.MemoryStats{
			MaxUsage: memory,
			Usage:    memory,
			RSS:      memory,
			Measured: measuredMemStats,
		}
		h.stateLock.Unlock()

		// update uasge
		usage := drivers.TaskResourceUsage{
			ResourceUsage: &drivers.ResourceUsage{
				CpuStats:    cs,
				MemoryStats: ms,
			},
			Timestamp: t.UTC().UnixNano(),
		}
		// send stats to nomad
		statsChannel <- &usage
	}
}
