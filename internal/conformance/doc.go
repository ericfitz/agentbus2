// Package conformance holds the Store and Bus conformance suites, in the
// subpackages store and bus. Each suite runs every case against both SQLite and
// Postgres (Decision 54), so a behavior difference between the engines fails a
// test. The suites are empty scaffolds in Milestone 1: each opens and queries
// both engines, so an unreachable engine fails them. Milestone 2 adds the Store
// cases and Milestone 3 the core Bus cases.
//
// make test excludes this tree (it needs Postgres); make test-conformance runs
// it, and make verify runs both.
package conformance
