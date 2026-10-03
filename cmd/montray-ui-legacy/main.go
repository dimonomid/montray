package main

import "os"

// main executes the Montray UI Legacy command-line application.
func main() {
	if err := newWatchRootCommand().Execute(); err != nil {
		// Cobra already printed the error message.
		os.Exit(1)
	}
}
