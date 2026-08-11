package main

import (
	playwright "github.com/mxschmitt/playwright-go"
	"log"
)

func main() {
	if err := playwright.Install(&playwright.RunOptions{SkipInstallBrowsers: true, NoInstallShell: true, Verbose: true}); err != nil {
		log.Fatal(err)
	}
}
