// Package health builds the tiered health document (spec
// docs/superpowers/specs/2026-10-25-health-design.md, issue #11) over the
// Store, the server notices, the embedding report that channels.Status shares,
// channels.Usage and this process's last maintenance tick. The caller's
// identity row decides the tier (Decision 73): no identity is the probe tier,
// a registered identity the identity tier, an administrator everything.
package health
