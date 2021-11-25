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
		// Teleport Terminal.
		"alex-kovoy": Reviewer{Group: "Terminal", Set: "A"},
		"kimlisa":    Reviewer{Group: "Terminal", Set: "A"},
		"gzdunek":    Reviewer{Group: "Terminal", Set: "B"},
		"rudream":    Reviewer{Group: "Terminal", Set: "B"},

		// Core.
		"codingllama":   Reviewer{Group: "Core", Set: "A"},
		"fspmarshall":   Reviewer{Group: "Core", Set: "A"},
		"nklaassen":     Reviewer{Group: "Core", Set: "A"},
		"r0mant":        Reviewer{Group: "Core", Set: "A"},
		"rosstimothy":   Reviewer{Group: "Core", Set: "A"},
		"smallinsky":    Reviewer{Group: "Core", Set: "A"},
		"timothyb89":    Reviewer{Group: "Core", Set: "A"},
		"zmb3":          Reviewer{Group: "Core", Set: "A"},
		"atburke":       Reviewer{Group: "Core", Set: "B"},
		"gabrielcorado": Reviewer{Group: "Core", Set: "B"},
		"greedy52":      Reviewer{Group: "Core", Set: "B"},
		"ibeckermayer":  Reviewer{Group: "Core", Set: "B"},
		"joerger":       Reviewer{Group: "Core", Set: "B"},
		"quinqu":        Reviewer{Group: "Core", Set: "B"},
		"tcsc":          Reviewer{Group: "Core", Set: "B"},
		"xacrimon":      Reviewer{Group: "Core", Set: "B"},

		// Internal.
		"aelkugia":             Reviewer{Group: "Internal"},
		"aharic":               Reviewer{Group: "Internal"},
		"alexwolfe":            Reviewer{Group: "Internal"},
		"annabambi":            Reviewer{Group: "Internal"},
		"bernardjkim":          Reviewer{Group: "Internal"},
		"c-styr":               Reviewer{Group: "Internal"},
		"dboslee":              Reviewer{Group: "Internal"},
		"deliaconstantino":     Reviewer{Group: "Internal"},
		"justinas":             Reviewer{Group: "Internal"},
		"kapilville":           Reviewer{Group: "Internal"},
		"kbence":               Reviewer{Group: "Internal"},
		"knisbet":              Reviewer{Group: "Internal"},
		"klizhentas":           Reviewer{Group: "Internal"},
		"logand22":             Reviewer{Group: "Internal"},
		"michaelmcallister":    Reviewer{Group: "Internal"},
		"mike-battle":          Reviewer{Group: "Internal"},
		"najiobeid":            Reviewer{Group: "Internal"},
		"nataliestaud":         Reviewer{Group: "Internal"},
		"pierrebeaucamp":       Reviewer{Group: "Internal"},
		"programmerq":          Reviewer{Group: "Internal"},
		"pschisa":              Reviewer{Group: "Internal"},
		"recruitingthebest":    Reviewer{Group: "Internal"},
		"rishibarbhaya-design": Reviewer{Group: "Internal"},
		"russjones":            Reviewer{Group: "Internal"},
		"sandylcruz":           Reviewer{Group: "Internal"},
		"sshahcodes":           Reviewer{Group: "Internal"},
		"stevengravy":          Reviewer{Group: "Internal"},
		"travelton":            Reviewer{Group: "Internal"},
		"travisgary":           Reviewer{Group: "Internal"},
		"ulysseskan":           Reviewer{Group: "Internal"},
		"valien":               Reviewer{Group: "Internal"},
		"wadells":              Reviewer{Group: "Internal"},
		"webvictim":            Reviewer{Group: "Internal"},
		"williamloy":           Reviewer{Group: "Internal"},
		"yjperez":              Reviewer{Group: "Internal"},
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
