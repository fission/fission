package util

import (
	"fmt"

	yaml "gopkg.in/yaml.v2"

	"github.com/fission/fission/pkg/controller/client"
	"github.com/fission/fission/pkg/fission-cli/log"
	"github.com/fission/fission/pkg/fission-cli/plugin"
	"github.com/fission/fission/pkg/info"
)

// Versions is a container of versions of the client (and its plugins) and server (and its plugins).
type Versions struct {
	Client map[string]info.BuildMeta `json:"client"`
	Server map[string]info.BuildMeta `json:"server"`
}

func GetVersion(client *client.Client) []byte {
	// Fetch client versions
	versions := Versions{
		Client: map[string]info.BuildMeta{
			"fission/core": info.BuildInfo(),
		},
	}

	for _, pmd := range plugin.FindAll() {
		versions.Client[pmd.Name] = info.BuildMeta{
			Version: pmd.Version,
		}
	}

	serverInfo, err := client.ServerInfo()
	if err != nil {
		log.Warn(fmt.Sprintf("Error getting Fission API version: %v", err))
		serverInfo = &info.ServerInfo{}
	}

	// Fetch server versions
	versions.Server = map[string]info.BuildMeta{
		"fission/core": serverInfo.Build,
	}

	// FUTURE: fetch versions of plugins server-side
	bs, err := yaml.Marshal(versions)
	if err != nil {
		log.Fatal("Failed to format versions: " + err.Error())
	}

	return bs
}
