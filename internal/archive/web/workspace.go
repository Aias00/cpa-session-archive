package workspace

import _ "embed"

//go:embed index.html
var workspaceHTML []byte

// HTML returns the bundled session workspace. Callers must not modify it.
func HTML() []byte { return workspaceHTML }
