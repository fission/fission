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

package fission

func (f Function) Key() string {
	return f.Metadata.Name
}

func (e Environment) Key() string {
	return e.Metadata.Name
}

func (ht HTTPTrigger) Key() string {
	return ht.Metadata.Name
}

func (mqt MessageQueueTrigger) Key() string {
	return mqt.Metadata.Name
}

func (tt TimeTrigger) Key() string {
	return tt.Metadata.Name
}

func (w Watch) Key() string {
	return w.Metadata.Name
}
