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

package reviewer

//func TestGetReviewerSets(t *testing.T) {
//	// Team only assignment.
//	setA, setB := getReviewerSets("alex-kovoy", []string{"Terminal"})
//	require.ElementsMatch(t, setA, []string{"kimlisa"})
//	require.ElementsMatch(t, setB, []string{"gzdunek", "rudream"})
//
//	// Cross-team assignment.
//	setA, setB = getReviewerSets("r0mant", []string{"Core", "Database Access"})
//	require.ElementsMatch(t, setA, []string{"smallinsky", "timothyb89", "rosstimothy", "codingllama", "zmb3", "fspmarshall"})
//	require.ElementsMatch(t, setB, []string{"quinqu", "atburke", "greedy52", "ibeckermayer", "gabrielcorado", "xacrimon"})
//}
//
//func TestClient(t *testing.T) {
//	client := github.NewClient(nil)
//	////reviews, _, err := client.PullRequests.ListReviews(context.Background(), "gravitational", "teleport", 9081, &github.ListOptions{
//	//reviews, _, err := client.PullRequests.ListReviews(context.Background(), "gravitational", "teleport", 9047, &github.ListOptions{
//	//	PerPage: 100,
//	//})
//	//require.NoError(t, err)
//	//fmt.Printf("--> reviews: %v.\n", reviews)
//	//for _, review := range reviews {
//	//	fmt.Printf("--> %v.\n", review)
//	//}
//
//	//pr, _, err := client.PullRequests.Get(context.Background(), "gravitational", "teleport", 9081)
//	//require.NoError(t, err)
//	//for _, reviewer := range pr.RequestedReviewers {
//	//	fmt.Printf("--> reviewer: %v.\n", reviewer)
//	//}
//
//	reviewers, _, err := client.PullRequests.ListReviewers(context.Background(), "gravitational", "teleport", 9081, &github.ListOptions{
//		PerPage: 100,
//	})
//	require.NoError(t, err)
//
//	for _, reviewer := range reviewers.Users {
//		fmt.Printf("--> reviewer: %v.\n", reviewer)
//	}
//
//}
