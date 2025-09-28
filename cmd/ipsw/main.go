package main

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"
	"ipsw/cmd/ipsw/decompile"
)

var rootCmd = &cobra.Command{
	Use:   "ipsw",
	Short: "A powerful tool for iOS research.",
	Long: `ipsw is a command-line tool for interacting with iOS firmware files,
and includes advanced features like the Odin decompilation engine.`,
}

func init() {
	// Add the decompile-project command to the root command.
	rootCmd.AddCommand(decompile.DecompileCmd)
}

func main() {
	if err := rootCmd.Execute(); err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}
}