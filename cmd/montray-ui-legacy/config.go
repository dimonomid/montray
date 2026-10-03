package main

import (
	"io/ioutil"
	"os"

	"github.com/dimonomid/montray/wsclient"
	"github.com/juju/errors"
	"gopkg.in/yaml.v2"
)

type config struct {
	WSClient wsclient.Config `yaml:"wsClient"`
}

func configNotFound(err error) bool {
	return os.IsNotExist(errors.Cause(err))
}

func loadConfig(filename string) (*config, error) {
	data, err := ioutil.ReadFile(filename)
	if err != nil {
		return nil, errors.Trace(err)
	}

	var cfg config

	if err := yaml.UnmarshalStrict(data, &cfg); err != nil {
		return nil, errors.Trace(err)
	}
	if err := cfg.WSClient.Validate(); err != nil {
		return nil, errors.Trace(err)
	}

	return &cfg, nil
}
