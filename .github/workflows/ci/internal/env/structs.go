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

package env

type Event struct {
	//Action string `json:"action"`

	Repository Repository `json:"repository"`

	PullRequest PullRequest `json:"pull_request"`

	//Review *Review `json:"review,omitempty"`
}

type Repository struct {
	Name  string `json:"name"`
	Owner Owner  `json:"owner"`
}

type Owner struct {
	Login string `json:"login"`
}

type PullRequest struct {
	User   User `json:"user"`
	Number int  `json:"number"`
	Head   Head `json:"head"`
	Base   Base `json:"base"`
}

type User struct {
	Login string `json:"login"`
}

type Head struct {
	SHA string `json:"sha"`
	Ref string `json:"ref"`
}

type Base struct {
	SHA string `json:"sha"`
	Ref string `json:"ref"`
}

//// PushEvent is used for unmarshalling push events
//type PushEvent struct {
//	Number      int        `json:"number"`
//	PullRequest PR         `json:"pull_request"`
//	Repository  Repository `json:"repository"`
//	CommitSHA   string     `json:"after"`
//	BeforeSHA   string     `json:"before"`
//}
//
//// PullRequestEvent s used for unmarshalling pull request events
//type PullRequestEvent struct {
//	Number      int        `json:"number"`
//	PullRequest PR         `json:"pull_request"`
//	Repository  Repository `json:"repository"`
//}
//
//// ReviewEvent contains metadata about the pull request
//// review (used for the pull request review event)
//type ReviewEvent struct {
//	Review      Review      `json:"review"`
//	Repository  Repository  `json:"repository"`
//	PullRequest PullRequest `json:"pull_request"`
//}

//// Review contains information about the pull request review
//type Review struct {
//	User User `json:"user"`
//}
//
//// PR contains information about the pull request (used for the pull request event)
//type PR struct {
//	User User
//	Head Head `json:"head"`
//	Base Base `json:"base"`
//}

//// action represents the current action
//type action struct {
//	Action string `json:"action"`
//}
