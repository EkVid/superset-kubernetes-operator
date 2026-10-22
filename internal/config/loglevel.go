/*
Licensed to the Apache Software Foundation (ASF) under one
or more contributor license agreements.  See the NOTICE file
distributed with this work for additional information
regarding copyright ownership.  The ASF licenses this file
to you under the Apache License, Version 2.0 (the
"License"); you may not use this file except in compliance
with the License.  You may obtain a copy of the License at

  http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing,
software distributed under the License is distributed on an
"AS IS" BASIS, WITHOUT WARRANTIES OR CONDITIONS OF ANY
KIND, either express or implied.  See the License for the
specific language governing permissions and limitations
under the License.
*/

// REMOVE THE COMMENTS BELOW DURING CLEANUP. JUST FOR UNDERSTANDING PURPOSES.

// This file works out a component's log level and converts it into the format
// that each program in that component understands.
// No k8s call happens in here.

// CRD accepts one set of lowercase levels (debug, info, warning, etc).
// But Superset, Gunicorn and Celery each expect the level in a different form,
// so this file converts the CRD level into the form each program expects.

package config

import (
	"fmt"
	"strings"

	v1alpha1 "github.com/apache/superset-kubernetes-operator/api/v1alpha1"
)

// applies when neither the component nor spec.logLevel sets a level.
const DefaultLogLevel = "info"

// returns the effective lowercase level: the component value,
// then the top level value, then DefaultLogLevel.
func ResolveLogLevel(component, topLevel *v1alpha1.LogLevel) string {
	if component != nil {
		return string(*component)
	}
	if topLevel != nil {
		return string(*topLevel)
	}
	return DefaultLogLevel
}

// returns the level name Python's logging module expects.
func PythonLogLevel(level string) string {
	return strings.ToUpper(level)
}

// returns the Celery worker/beat --loglevel argument.
func CeleryLogLevelFlag(level string) string {
	return fmt.Sprintf("--loglevel=%s", level)
}
