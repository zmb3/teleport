package bot

import (
	"math/rand"
	"time"

	"github.com/gravitational/trace"
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
		"kimlisa":    reviewer{group: "Terminal", set: "B"},
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
)

// getReviewers returns a list of reviewers to assign for a particular user.
func getReviewers(name string) ([]string, error) {
	v, ok := codeReviewers[name]
	if !ok {
		return nil, trace.BadParameter("invalid reviewer %v", name)
	}

	switch v.group {
	// For non-core team members, assign to team leads for dispatch.
	case "Internal":
		return []string{"r0mant", "russjones", "zmb3"}, nil
	// For non-subteam core, randomly assign.
	case "Core":
		return assign(name, "Core")
	// For subteams, assign within the subteam most of the time.
	default:
		if rand.Intn(9) > 7 {
			return assign(name, "Core")
		}
		return assign(name, v.group)
	}
}

func assign(name string, skipGroup string) ([]string, error) {
	var setA []string
	var setB []string

	for k, v := range codeReviewers {
		if v.group != skipGroup {
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

	return []string{
		setA[rand.Intn(len(setA))],
		setB[rand.Intn(len(setB))],
	}, nil
}
