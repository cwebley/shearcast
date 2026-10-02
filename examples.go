// Package shearcast holds the starter files that `shearcast init` writes.
package shearcast

import _ "embed"

//go:embed config.example.toml
var ConfigExample []byte

//go:embed .env.example
var EnvExample []byte
