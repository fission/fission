// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package info

import (
	"encoding/json"
	"time"
)

var (
	GitCommit string // $(git rev-parse HEAD) (1b4716ab84903b2e477135a3dc5afdb07f685cb7)
	BuildDate string // $(date -u +'%Y-%m-%dT%H:%M:%SZ') (2018-03-08T18:54:38Z)
	Version   string // fission release version (0.6.0)
)

type (
	BuildMeta struct {
		GitCommit string `json:"GitCommit,omitempty"`
		BuildDate string `json:"BuildDate,omitempty"`
		Version   string `json:"Version,omitempty"`
	}

	Time struct {
		Timezone    string    `json:"Timezone,omitempty"`
		CurrentTime time.Time `json:"CurrentTime"`
	}

	ServerInfo struct {
		Build      BuildMeta `json:"Build"`
		ServerTime Time      `json:"ServerTime"`
	}

	// Versions is a container of versions of the client (and its plugins) and server (and its plugins).
	Versions struct {
		Client map[string]BuildMeta `json:"client"`
		Server map[string]BuildMeta `json:"server"`
	}
)

func BuildInfo() BuildMeta {
	return BuildMeta{
		GitCommit: GitCommit,
		BuildDate: BuildDate,
		Version:   Version,
	}
}

func (info BuildMeta) String() string {
	v, _ := json.Marshal(info)
	return string(v)
}

func TimeInfo() Time {
	t := time.Now()
	zone, _ := t.Local().Zone()
	return Time{
		Timezone:    zone,
		CurrentTime: t,
	}
}

func ApiInfo() ServerInfo {
	return ServerInfo{
		Build:      BuildInfo(),
		ServerTime: TimeInfo(),
	}
}

func (info ServerInfo) String() string {
	v, _ := json.Marshal(info)
	return string(v)
}
