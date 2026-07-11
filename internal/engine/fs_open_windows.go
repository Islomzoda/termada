//go:build windows

package engine

import (
	"fmt"
	"os"
)

// Windows needs a CreateFile/FILE_FLAG_OPEN_REPARSE_POINT implementation to
// provide the same descriptor-walk guarantee. Until then fail closed instead of
// silently reverting to a pathname check with a symlink race.
func openLocalNoSymlink(path string, write, appendMode bool) (*os.File, error) {
	return nil, fmt.Errorf("secure local file access is not supported on windows")
}
