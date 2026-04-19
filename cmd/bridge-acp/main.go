package main

import (
	"flag"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"bridge-acp/internal/config"
	"bridge-acp/internal/server"
)

var (
	configPath string
	version    = "0.1.0"
)

func main() {
	flag.StringVar(&configPath, "config", "", "Path to configuration file")
	flag.BoolVar(&showVersion, "version", false, "Show version")
	flag.Parse()

	if showVersion {
		fmt.Printf("bridge-acp version %s\n", version)
		os.Exit(0)
	}

	// Load configuration
	cfg, err := config.Load(configPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error loading config: %v\n", err)
		os.Exit(1)
	}

	fmt.Printf("Starting bridge-acp server on %s\n", cfg.Listen)
	fmt.Printf("CLI command: %s %v\n", cfg.CLI.Command, cfg.CLI.Args)
	fmt.Printf("Workspace: %s\n", cfg.CLI.Workspace)
	fmt.Printf("Model: %s\n", cfg.Model)

	// Create server
	srv, err := server.New(cfg)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error creating server: %v\n", err)
		os.Exit(1)
	}

	// Handle shutdown
	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, syscall.SIGINT, syscall.SIGTERM)

	go func() {
		<-sigChan
		fmt.Println("\nShutting down...")
		srv.Close()
		os.Exit(0)
	}()

	// Start server
	if err := srv.Run(); err != nil {
		fmt.Fprintf(os.Stderr, "Error running server: %v\n", err)
		os.Exit(1)
	}
}

var showVersion bool
