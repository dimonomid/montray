package core

import (
	"github.com/dimonomid/montray/backend/collectors/exec"
	"github.com/dimonomid/montray/backend/collectors/systemd"
	"github.com/dimonomid/montray/backend/messengers/filelogger"
	"github.com/dimonomid/montray/backend/messengers/webserver"
)

type Config struct {
	Collectors []Collector `yaml:"collectors"`
	Messengers []Messenger `yaml:"messengers"`
}

type Collector struct {
	// ID is just an arbitrary string which uniquely identifies collector.
	// It must only contain lowercase English letters, numbers, hyphens and
	// underscores, and it must start with a letter.
	//
	// All item keys from that collector will be prefixed with that ID and a dot;
	// e.g. systemd collector returns items with keys like "gpg-agent.service";
	// and if ID is "mysystemd", that key becomes "mysystemd.gpg-agent.service".
	ID string

	// Below are the fields for all possible collector types. Exactly one of them
	// must be non-nil.

	Systemd *systemd.Config `yaml:"systemd"`
	Exec    *exec.Config    `yaml:"exec"`
}

type Messenger struct {
	FileLogger *filelogger.Config `yaml:"fileLogger"`
	Webserver  *webserver.Config  `yaml:"webserver"`

	// TODO: implement other messengers, like emailer, slacker, etc.
}
