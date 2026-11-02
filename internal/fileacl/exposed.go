package fileacl

import "errors"

// ErrExposed marks CheckOwnerOnly's policy refusals (a NULL DACL, an entry for
// another principal), as opposed to a failure to read the file's security.
var ErrExposed = errors.New("fileacl: the file is accessible to other principals")
