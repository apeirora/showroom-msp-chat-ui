// Package webuiguard supplies the compatibility guard for the pinned WebUI image.
package webuiguard

import _ "embed"

// Source is mounted read-only; changing it versions the immutable access bundle.
//
//go:embed chatui_guard.py
var Source []byte
