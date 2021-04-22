package main

import (
	"context"
	"fmt"
	"os"

	"github.com/gravitational/teleport/assets/statusctl/pkg/commands"
	"github.com/gravitational/teleport/assets/statusctl/pkg/config"
	"github.com/gravitational/teleport/assets/statusctl/pkg/constants"
)

func usage() {
	fmt.Printf("usage: %v [pr|milestone] [-v].\n", os.Args[0])
}

func main() {
	// Parse command line arguments and flags.
	if len(os.Args) == 1 {
		usage()
		os.Exit(1)
	}

	// Read in GitHub access token. An access token is not required, but GitHub
	// rate limits requests fairly heavily if one is not provided.
	accessToken, err := config.ReadToken()
	if err != nil {
		fmt.Printf("No GitHub OAuth2 token found, requests will be rate limited.\n")
	}

	// Parse command line flags.

	// Create a GitHub client.
	client, err := commands.NewClient(context.Background(), &commands.Config{
		AccessToken: accessToken,
	})
	if err != nil {
		fmt.Printf("Failed to create client: %v.\n", err)
		os.Exit(1)
	}

	// Issue command.
	switch os.Args[1] {
	case constants.PR:
		err = client.Pulls(context.Background())
	case constants.Milestone:
		err = client.Milestones(context.Background())
	}
	if err != nil {
		fmt.Printf("Command failed: %v.\n", err)
		os.Exit(1)
	}
}
