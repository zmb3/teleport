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

package review

var config = &Config{
	CodeReviewers: map[string]Reviewer{
		// Database Access.
		"r0mant":        Reviewer{Group: "Database Access", Set: "A"},
		"smallinsky":    Reviewer{Group: "Database Access", Set: "A"},
		"greedy52":      Reviewer{Group: "Database Access", Set: "B"},
		"gabrielcorado": Reviewer{Group: "Database Access", Set: "B"},

		// Teleport Terminal.
		"alex-kovoy": Reviewer{Group: "Terminal", Set: "A"},
		"kimlisa":    Reviewer{Group: "Terminal", Set: "A"},
		"gzdunek":    Reviewer{Group: "Terminal", Set: "B"},
		"rudream":    Reviewer{Group: "Terminal", Set: "B"},

		// Core.
		"codingllama":  Reviewer{Group: "Core", Set: "A"},
		"nklaassen":    Reviewer{Group: "Core", Set: "A"},
		"fspmarshall":  Reviewer{Group: "Core", Set: "A"},
		"rosstimothy":  Reviewer{Group: "Core", Set: "A"},
		"timothyb89":   Reviewer{Group: "Core", Set: "A"},
		"zmb3":         Reviewer{Group: "Core", Set: "A"},
		"xacrimon":     Reviewer{Group: "Core", Set: "B"},
		"ibeckermayer": Reviewer{Group: "Core", Set: "B"},
		"tcsc":         Reviewer{Group: "Core", Set: "B"},
		"quinqu":       Reviewer{Group: "Core", Set: "B"},
		"joerger":      Reviewer{Group: "Core", Set: "B"},
		"atburke":      Reviewer{Group: "Core", Set: "B"},

		// Internal.
		"aelkugia":             Reviewer{Group: "Internal", Set: ""},
		"aharic":               Reviewer{Group: "Internal", Set: ""},
		"alexwolfe":            Reviewer{Group: "Internal", Set: ""},
		"annabambi":            Reviewer{Group: "Internal", Set: ""},
		"bernardjkim":          Reviewer{Group: "Internal", Set: ""},
		"c-styr":               Reviewer{Group: "Internal", Set: ""},
		"dboslee":              Reviewer{Group: "Internal", Set: ""},
		"deliaconstantino":     Reviewer{Group: "Internal", Set: ""},
		"justinas":             Reviewer{Group: "Internal", Set: ""},
		"kapilville":           Reviewer{Group: "Internal", Set: ""},
		"kbence":               Reviewer{Group: "Internal", Set: ""},
		"knisbet":              Reviewer{Group: "Internal", Set: ""},
		"logand22":             Reviewer{Group: "Internal", Set: ""},
		"michaelmcallister":    Reviewer{Group: "Internal", Set: ""},
		"mike-battle":          Reviewer{Group: "Internal", Set: ""},
		"najiobeid":            Reviewer{Group: "Internal", Set: ""},
		"nataliestaud":         Reviewer{Group: "Internal", Set: ""},
		"pierrebeaucamp":       Reviewer{Group: "Internal", Set: ""},
		"programmerq":          Reviewer{Group: "Internal", Set: ""},
		"pschisa":              Reviewer{Group: "Internal", Set: ""},
		"recruitingthebest":    Reviewer{Group: "Internal", Set: ""},
		"rishibarbhaya-design": Reviewer{Group: "Internal", Set: ""},
		"sandylcruz":           Reviewer{Group: "Internal", Set: ""},
		"sshahcodes":           Reviewer{Group: "Internal", Set: ""},
		"stevengravy":          Reviewer{Group: "Internal", Set: ""},
		"travelton":            Reviewer{Group: "Internal", Set: ""},
		"travisgary":           Reviewer{Group: "Internal", Set: ""},
		"ulysseskan":           Reviewer{Group: "Internal", Set: ""},
		"valien":               Reviewer{Group: "Internal", Set: ""},
		"wadells":              Reviewer{Group: "Internal", Set: ""},
		"webvictim":            Reviewer{Group: "Internal", Set: ""},
		"williamloy":           Reviewer{Group: "Internal", Set: ""},
		"yjperez":              Reviewer{Group: "Internal", Set: ""},
	},

	CodeReviewersOmit: map[string]bool{
		// Martians.
		"joerger": false,
		"tcsc":    false,
		// OOO.
		"nklaassen": false,
	},

	DocsReviewers: map[string]Reviewer{
		"klizhentas": Reviewer{Group: "Core", Set: "A"},
	},

	DocsReviewersOmit: map[string]bool{},

	DefaultReviewers: []string{
		"r0mant",
		"russjones",
		"zmb3",
	},
}

type Reviewer struct {
	Group string
	Set   string
}
