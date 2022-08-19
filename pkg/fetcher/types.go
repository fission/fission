/*
Copyright 2016 The Fission Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package fetcher

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	fv1 "github.com/fission/fission/pkg/apis/core/v1"
)

// Fission-Environment interface. The following types are not
// exposed in the Fission API, but rather used by Fission to
// talk to environments.
type (
	FetchRequestType int

	FunctionSpecializeRequest struct {
		FetchReq FunctionFetchRequest
		LoadReq  FunctionLoadRequest
	}

	FunctionFetchRequest struct {
		FetchType     FetchRequestType         `json:"fetchType"`
		Package       metav1.ObjectMeta        `json:"package"`
		Url           string                   `json:"url"`
		StorageSvcUrl string                   `json:"storagesvcurl"`
		Filename      string                   `json:"filename"`
		Secrets       []fv1.SecretReference    `json:"secretList"`
		ConfigMaps    []fv1.ConfigMapReference `json:"configMapList"`
		KeepArchive   bool                     `json:"keeparchive"`
	}

	FunctionLoadRequest struct {
		// FilePath is an absolute filesystem path to the
		// function. What exactly is stored here is
		// env-specific. Optional.
		FilePath string `json:"filepath"`

		// FunctionName has an environment-specific meaning;
		// usually, it defines a function within a module
		// containing multiple functions. Optional; default is
		// environment-specific.
		FunctionName string `json:"functionName"`

		// URL to expose this function at. Optional; defaults
		// to "/".
		URL string `json:"url"`

		// Metadata
		FunctionMetadata *metav1.ObjectMeta

		EnvVersion int `json:"envVersion"`
	}

	// ArchiveUploadRequest send from builder manager describes which
	// deployment package should be upload to storage service.
	ArchiveUploadRequest struct {
		Filename       string `json:"filename"`
		StorageSvcUrl  string `json:"storagesvcurl"`
		ArchivePackage bool   `json:"archivepackage"`
	}

	// ArchiveUploadResponse defines the download url of an archive and
	// its checksum.
	ArchiveUploadResponse struct {
		ArchiveDownloadUrl string       `json:"archiveDownloadUrl"`
		Checksum           fv1.Checksum `json:"checksum"`
	}
)
