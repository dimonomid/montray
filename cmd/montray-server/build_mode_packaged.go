//go:build packaged

package main

// Packaged builds keep the setup command so it can explain why setup is unavailable.
const packagedBuild = true
const buildMode = "for packaging"
