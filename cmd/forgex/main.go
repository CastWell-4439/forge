package main

import (
	"fmt"
	"os"
)

const version = "ForgeX v0.1.0"

func main() {
	args := os.Args[1:]
	if len(args) == 0 {
		printHelp()
		return
	}

	switch args[0] {
	case "version", "--version", "-v":
		fmt.Println(version)
	case "help", "--help", "-h":
		printHelp()
	default:
		fmt.Fprintf(os.Stderr, "unknown command: %s\n\n", args[0])
		printHelp()
		os.Exit(2)
	}
}

func printHelp() {
	fmt.Println(`ForgeX - Agent Harness Control Plane

Usage:
  forgex <command>

Commands:
  version    Print ForgeX version
  help       Print this help message

Milestone:
  M0/M1 only: project skeleton and model definitions.
`)
}
