package main

import "os"

// main executes the Montray Server command-line application.
func main() {
	if err := newRootCommand().Execute(); err != nil {
		// Cobra already printed the error message.
		os.Exit(1)
	}
}
