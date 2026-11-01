// Package schedule runs the periodic maintenance the Java server spreads over
// its schedule package: expiring accounts, deleting what has expired, sending
// the scheduled reports and checking the database.
//
// Every task is a plain function over small interfaces, so a test runs it once
// without waiting for a tick and without a database.
package schedule

import (
	"context"
	"sync"
	"time"
)

// Task is one periodic job.
type Task interface {
	// Name is the label used in logs and in the error counter.
	Name() string
	// Run performs one pass. An error is logged and counted; the scheduler keeps
	// running, because a database blip must not stop the maintenance.
	Run(ctx context.Context) error
}

// Scheduler runs the tasks on their own intervals.
type Scheduler struct {
	logf  func(format string, args ...any)
	onRun func(task string, err error)

	mu    sync.Mutex
	tasks []entry

	wg        sync.WaitGroup
	closeOnce sync.Once
}

type entry struct {
	task     Task
	interval time.Duration
	// runOnStart lets the first pass happen immediately, which is what a
	// maintenance task wants after a restart.
	runOnStart bool
}

// New creates a scheduler. The options report the outcome of every pass.
func New(options ...Option) *Scheduler {
	scheduler := &Scheduler{logf: func(string, ...any) {}}
	for _, option := range options {
		option(scheduler)
	}
	return scheduler
}

// Option customises a Scheduler.
type Option func(*Scheduler)

// WithLogger reports the task failures.
func WithLogger(logf func(format string, args ...any)) Option {
	return func(s *Scheduler) {
		if logf != nil {
			s.logf = logf
		}
	}
}

// WithObserver reports every pass, successful or not, to a metrics counter.
func WithObserver(onRun func(task string, err error)) Option {
	return func(s *Scheduler) {
		if onRun != nil {
			s.onRun = onRun
		}
	}
}

// Add registers a task. An interval of zero or less disables it, which is how a
// configuration key turns one task off.
func (s *Scheduler) Add(task Task, interval time.Duration, runOnStart bool) {
	if task == nil || interval <= 0 {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.tasks = append(s.tasks, entry{task: task, interval: interval, runOnStart: runOnStart})
}

// Tasks lists the registered task names, in registration order.
func (s *Scheduler) Tasks() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	names := make([]string, 0, len(s.tasks))
	for _, item := range s.tasks {
		names = append(names, item.task.Name())
	}
	return names
}

// Run starts one goroutine per task and returns when ctx is cancelled and every
// goroutine has stopped.
func (s *Scheduler) Run(ctx context.Context) {
	s.mu.Lock()
	tasks := append([]entry(nil), s.tasks...)
	s.mu.Unlock()

	for _, item := range tasks {
		s.wg.Add(1)
		go func(item entry) {
			defer s.wg.Done()
			s.loop(ctx, item)
		}(item)
	}
	s.wg.Wait()
}

// loop runs one task forever.
func (s *Scheduler) loop(ctx context.Context, item entry) {
	if item.runOnStart {
		s.runOnce(ctx, item.task)
	}

	ticker := time.NewTicker(item.interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			s.runOnce(ctx, item.task)
		}
	}
}

// runOnce performs one pass and reports it. The task gets the scheduler context,
// so cancelling the gateway stops a task mid-flight too.
func (s *Scheduler) runOnce(ctx context.Context, task Task) {
	if ctx.Err() != nil {
		return
	}
	err := task.Run(ctx)
	if err != nil && ctx.Err() == nil {
		s.logf("schedule: %s: %v", task.Name(), err)
	}
	if s.onRun != nil {
		s.onRun(task.Name(), err)
	}
}

// RunOnce executes one task now. It backs the tests and the "run the reports
// task from the command line" use case.
func (s *Scheduler) RunOnce(ctx context.Context, name string) error {
	s.mu.Lock()
	var found Task
	for _, item := range s.tasks {
		if item.task.Name() == name {
			found = item.task
			break
		}
	}
	s.mu.Unlock()
	if found == nil {
		return ErrUnknownTask
	}
	return found.Run(ctx)
}

// Close stops the scheduler. It is idempotent, so the shutdown path can call it
// even when Run never started.
func (s *Scheduler) Close() {
	s.closeOnce.Do(func() {})
}
