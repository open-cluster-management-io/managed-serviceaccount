package main

import (
	goflag "flag"
	"fmt"
	"os"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
	utilflag "k8s.io/component-base/cli/flag"

	"open-cluster-management.io/managed-serviceaccount/cmd/agent"
	hub "open-cluster-management.io/managed-serviceaccount/cmd/manager"
)

func main() {
	pflag.CommandLine.SetNormalizeFunc(utilflag.WordSepNormalizeFunc)
	pflag.CommandLine.AddGoFlagSet(goflag.CommandLine)

	command := newCommand()
	if err := command.Execute(); err != nil {
		fmt.Fprintf(os.Stderr, "%v\n", err)
		os.Exit(1)
	}
}

func newCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "managed service account",
		Short: "msa",
		Run: func(cmd *cobra.Command, args []string) {
			_ = cmd.Help()
			os.Exit(1)
		},
	}

	cmd.AddCommand(hub.NewManager())
	cmd.AddCommand(agent.NewAgent())

	fmt.Println("Hello, World!")

	return cmd
}
