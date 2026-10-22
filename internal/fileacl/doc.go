// Package fileacl sets the owner-only access a file needs on Windows, where
// a Unix mode means nothing: credential files, the bearer cache (Decision 55)
// and the server's unix socket (Decision 58). It is a leaf: it imports
// nothing from this module.
package fileacl
