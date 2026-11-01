package schedule

import "errors"

// ErrUnknownTask is returned by RunOnce when no task has that name.
var ErrUnknownTask = errors.New("schedule: unknown task")
