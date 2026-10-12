// Command piccolo is a thin setup and launcher for PicoClaw: init creates a
// managed workspace, doctor validates it, serve runs the PicoClaw gateway
// container.
package main

import "os"

func main() {
	if err := newRootCmd().Execute(); err != nil {
		os.Exit(1)
	}
}
