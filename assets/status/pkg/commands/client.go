package commands

import (
	"context"
	"net/http"

	"github.com/gravitational/trace"

	"github.com/google/go-github/v35/github"
	"golang.org/x/oauth2"
)

type Config struct {
	AccessToken string
	Verbose     bool
}

func (c *Config) CheckAndSetDefaults() error {
	if c.Verbose && c.AccessToken == "" {
		return trace.BadParameter("verbose unavailable without an access token")
	}

	return nil
}

type Client struct {
	c *Config

	client *github.Client
}

func NewClient(ctx context.Context, c *Config) (*Client, error) {
	if err := c.CheckAndSetDefaults(); err != nil {
		return nil, trace.Wrap(err)
	}

	// If an access token was provided, create an authenticated client, otherwise
	// return an (rate limited) unauthenticated client.
	var oauth *http.Client
	if c.AccessToken != "" {
		oauth = oauth2.NewClient(ctx, oauth2.StaticTokenSource(
			&oauth2.Token{AccessToken: c.AccessToken},
		))

	}

	return &Client{
		c:      c,
		client: github.NewClient(oauth),
	}, nil
}
