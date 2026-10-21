package bus

// Bus is the composite of the five consumer interfaces. It compiles only
// while no two embedded interfaces declare one name with different
// signatures.
type Bus interface {
	Messaging
	Sessions
	Memories
	Tasks
	Admin
}
