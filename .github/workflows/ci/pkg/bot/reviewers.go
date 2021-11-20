/*
Copyright 2021 Gravitational, Inc.

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

package bot

import (
	"math/rand"
	"time"
)

func init() {
	rand.Seed(time.Now().UnixNano())
}

type reviewer struct {
	group string
	set   string
}

var (
	codeReviewers = map[string]reviewer{
		// Database Access.
		"r0mant":        reviewer{group: "Database Access", set: "A"},
		"smallinsky":    reviewer{group: "Database Access", set: "A"},
		"greedy52":      reviewer{group: "Database Access", set: "B"},
		"gabrielcorado": reviewer{group: "Database Access", set: "B"},

		// Teleport Terminal.
		"alex-kovoy": reviewer{group: "Terminal", set: "A"},
		"kimlisa":    reviewer{group: "Terminal", set: "A"},
		"gzdunek":    reviewer{group: "Terminal", set: "B"},
		"rudream":    reviewer{group: "Terminal", set: "B"},

		// Core.
		"codingllama":  reviewer{group: "Core", set: "A"},
		"nklaassen":    reviewer{group: "Core", set: "A"},
		"fspmarshall":  reviewer{group: "Core", set: "A"},
		"rosstimothy":  reviewer{group: "Core", set: "A"},
		"timothyb89":   reviewer{group: "Core", set: "A"},
		"zmb3":         reviewer{group: "Core", set: "A"},
		"xacrimon":     reviewer{group: "Core", set: "B"},
		"ibeckermayer": reviewer{group: "Core", set: "B"},
		"tcsc":         reviewer{group: "Core", set: "B"},
		"quinqu":       reviewer{group: "Core", set: "B"},
		"joerger":      reviewer{group: "Core", set: "B"},
		"atburke":      reviewer{group: "Core", set: "B"},

		// Internal.
		"aelkugia":             reviewer{group: "Internal", set: ""},
		"aharic":               reviewer{group: "Internal", set: ""},
		"alexwolfe":            reviewer{group: "Internal", set: ""},
		"annabambi":            reviewer{group: "Internal", set: ""},
		"bernardjkim":          reviewer{group: "Internal", set: ""},
		"c-styr":               reviewer{group: "Internal", set: ""},
		"dboslee":              reviewer{group: "Internal", set: ""},
		"deliaconstantino":     reviewer{group: "Internal", set: ""},
		"justinas":             reviewer{group: "Internal", set: ""},
		"kapilville":           reviewer{group: "Internal", set: ""},
		"kbence":               reviewer{group: "Internal", set: ""},
		"knisbet":              reviewer{group: "Internal", set: ""},
		"logand22":             reviewer{group: "Internal", set: ""},
		"michaelmcallister":    reviewer{group: "Internal", set: ""},
		"mike-battle":          reviewer{group: "Internal", set: ""},
		"najiobeid":            reviewer{group: "Internal", set: ""},
		"nataliestaud":         reviewer{group: "Internal", set: ""},
		"pierrebeaucamp":       reviewer{group: "Internal", set: ""},
		"programmerq":          reviewer{group: "Internal", set: ""},
		"pschisa":              reviewer{group: "Internal", set: ""},
		"recruitingthebest":    reviewer{group: "Internal", set: ""},
		"rishibarbhaya-design": reviewer{group: "Internal", set: ""},
		"sandylcruz":           reviewer{group: "Internal", set: ""},
		"sshahcodes":           reviewer{group: "Internal", set: ""},
		"stevengravy":          reviewer{group: "Internal", set: ""},
		"travelton":            reviewer{group: "Internal", set: ""},
		"travisgary":           reviewer{group: "Internal", set: ""},
		"ulysseskan":           reviewer{group: "Internal", set: ""},
		"valien":               reviewer{group: "Internal", set: ""},
		"wadells":              reviewer{group: "Internal", set: ""},
		"webvictim":            reviewer{group: "Internal", set: ""},
		"williamloy":           reviewer{group: "Internal", set: ""},
		"yjperez":              reviewer{group: "Internal", set: ""},
	}

	reviewerOmit = map[string]bool{
		// Martians.
		"joerger": false,
		"tcsc":    false,
		// OOO.
		"nklaassen": false,
	}

	defaultReviewers = []string{"r0mant", "russjones", "zmb3"}
)

// GetCodeReviewers returns a list of code reviewers for this author.
func GetCodeReviewers(name string) ([]string, error) {
	// External contributors get assign the default reviewer set. Default
	// reviewers will triage and re-assign.
	v, ok := codeReviewers[name]
	if !ok {
		return defaultReviewers, nil
	}
	return getCodeReviewers(name, v.group)
}

func getCodeReviewers(name string, group string) ([]string, error) {
	switch group {
	// Terminal team does own reviews.
	case "Terminal":
		return getReviewers(name, "Terminal")
	// Core and Database Access does internal team reviews most of the time,
	// however 30% of the time reviews are cross-team.
	case "Database Access", "Core":
		if rand.Intn(10) > 7 {
			return getReviewers(name, "Core", "Database Access")
		}
		return getReviewers(name, group)
	// Non-Core reviews get assigned to default reviews who will re-assign to
	// appropriate reviewers.
	default:
		return defaultReviewers, nil
	}
}

func getReviewers(name string, selectGroup ...string) ([]string, error) {
	// Get two sets of reviewers whose union is all potential reviewers.
	setA, setB := getReviewerSets(name, selectGroup)

	// Randomly select a reviewer from each set and return a pair of reviewers.
	return []string{
		setA[rand.Intn(len(setA))],
		setB[rand.Intn(len(setB))],
	}, nil
}

func getReviewerSets(name string, selectGroup []string) ([]string, []string) {
	var setA []string
	var setB []string

	for k, v := range codeReviewers {
		if skipGroup(v.group, selectGroup) {
			continue
		}
		if _, ok := reviewerOmit[k]; ok {
			continue
		}
		// Can not review own PR.
		if k == name {
			continue
		}

		if v.set == "A" {
			setA = append(setA, k)
		} else {
			setB = append(setB, k)
		}
	}

	return setA, setB
}

func skipGroup(group string, selectGroup []string) bool {
	for _, s := range selectGroup {
		if group == s {
			return false
		}
	}
	return true
}
