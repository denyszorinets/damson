// Package pluginsdk is the public SDK every Damson plugin is written against.
//
// Built-in plugins use exactly this API. A plugin package exports one
// constructor, Plugins() []pluginsdk.Plugin, uses no init() registration and
// keeps no package-level mutable state (ARCHITECTURE.md section 6).
//
// The SDK follows semver (see Version). Its only third-party dependency is the
// JSON Schema package used to generate input and output schemas.
package pluginsdk

// Version is the semantic version of this SDK. Breaking API changes require a
// new major version.
const Version = "0.1.0"
