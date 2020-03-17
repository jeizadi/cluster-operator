/*
Copyright 2019 The Kubernetes Authors.

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

package aliup

import (
	"github.com/infobloxopen/cluster-operator/kops/upup/pkg/fi"
)

type ALIAPITarget struct {
	Cloud ALICloud
}

var _ fi.Target = &ALIAPITarget{}

func NewALIAPITarget(cloud ALICloud) *ALIAPITarget {
	return &ALIAPITarget{
		Cloud: cloud,
	}
}

func (t *ALIAPITarget) Finish(taskMap map[string]fi.Task) error {
	return nil
}

func (t *ALIAPITarget) ProcessDeletions() bool {
	return true
}