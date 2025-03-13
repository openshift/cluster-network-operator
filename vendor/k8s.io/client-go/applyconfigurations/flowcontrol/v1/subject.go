/*
Copyright The Kubernetes Authors.

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

// Code generated by applyconfiguration-gen. DO NOT EDIT.

package v1

import (
	flowcontrolv1 "k8s.io/api/flowcontrol/v1"
)

// SubjectApplyConfiguration represents a declarative configuration of the Subject type for use
// with apply.
type SubjectApplyConfiguration struct {
	Kind           *flowcontrolv1.SubjectKind               `json:"kind,omitempty"`
	User           *UserSubjectApplyConfiguration           `json:"user,omitempty"`
	Group          *GroupSubjectApplyConfiguration          `json:"group,omitempty"`
	ServiceAccount *ServiceAccountSubjectApplyConfiguration `json:"serviceAccount,omitempty"`
}

// SubjectApplyConfiguration constructs a declarative configuration of the Subject type for use with
// apply.
func Subject() *SubjectApplyConfiguration {
	return &SubjectApplyConfiguration{}
}

// WithKind sets the Kind field in the declarative configuration to the given value
// and returns the receiver, so that objects can be built by chaining "With" function invocations.
// If called multiple times, the Kind field is set to the value of the last call.
func (b *SubjectApplyConfiguration) WithKind(value flowcontrolv1.SubjectKind) *SubjectApplyConfiguration {
	b.Kind = &value
	return b
}

// WithUser sets the User field in the declarative configuration to the given value
// and returns the receiver, so that objects can be built by chaining "With" function invocations.
// If called multiple times, the User field is set to the value of the last call.
func (b *SubjectApplyConfiguration) WithUser(value *UserSubjectApplyConfiguration) *SubjectApplyConfiguration {
	b.User = value
	return b
}

// WithGroup sets the Group field in the declarative configuration to the given value
// and returns the receiver, so that objects can be built by chaining "With" function invocations.
// If called multiple times, the Group field is set to the value of the last call.
func (b *SubjectApplyConfiguration) WithGroup(value *GroupSubjectApplyConfiguration) *SubjectApplyConfiguration {
	b.Group = value
	return b
}

// WithServiceAccount sets the ServiceAccount field in the declarative configuration to the given value
// and returns the receiver, so that objects can be built by chaining "With" function invocations.
// If called multiple times, the ServiceAccount field is set to the value of the last call.
func (b *SubjectApplyConfiguration) WithServiceAccount(value *ServiceAccountSubjectApplyConfiguration) *SubjectApplyConfiguration {
	b.ServiceAccount = value
	return b
}
