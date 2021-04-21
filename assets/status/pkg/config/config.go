package config

import (
	"io/ioutil"

	"github.com/gravitational/trace"
	"gopkg.in/yaml.v2"
)

type Config struct {
	Host Host `yaml:"github.com"`
}

type Host struct {
	User     string `yaml:"user"`
	Token    string `yaml:"oauth_token"`
	Protocol string `yaml:"git_protocol"`
}

// ReadToken returns the GitHub OAuth2 token configured by "gh" command.
func ReadToken() (string, error) {
	//os.UserHomeDir
	bytes, err := ioutil.ReadFile("/home/rjones/.config/gh/hosts.yml")
	if err != nil {
		return "", trace.Wrap(err)
	}

	var config Config
	if err := yaml.Unmarshal(bytes, &config); err != nil {
		return "", trace.Wrap(err)
	}

	return config.Host.Token, nil
}
