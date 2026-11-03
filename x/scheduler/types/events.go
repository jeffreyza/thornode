package types

// event types
//
//nolint:gosec
const (
	EventScheduleMsg  = "schedule_add"
	EventExecuteMsg   = "schedule_execute"
	EventExecuteStart = "schedule_start"
	EventExecuteEnd   = "schedule_end"

	AttributeAfter  = "after"
	AttributeHeight = "height"
	AttributeSender = "sender"
	AttributeMsg    = "msg"
	AttributeError  = "error"
)
