package bot

import (
	"context"
	"fmt"
	"testing"

	"github.com/google/go-github/v37/github"
	"github.com/stretchr/testify/require"
)

func TestGetReviewerSets(t *testing.T) {
	// Team only assignment.
	setA, setB := getReviewerSets("alex-kovoy", []string{"Terminal"})
	require.ElementsMatch(t, setA, []string{"kimlisa"})
	require.ElementsMatch(t, setB, []string{"gzdunek", "rudream"})

	// Cross-team assignment.
	setA, setB = getReviewerSets("r0mant", []string{"Core", "Database Access"})
	require.ElementsMatch(t, setA, []string{"smallinsky", "timothyb89", "rosstimothy", "codingllama", "zmb3", "fspmarshall"})
	require.ElementsMatch(t, setB, []string{"quinqu", "atburke", "greedy52", "ibeckermayer", "gabrielcorado", "xacrimon"})
}

func TestClient(t *testing.T) {
	client := github.NewClient(nil)
	////reviews, _, err := client.PullRequests.ListReviews(context.Background(), "gravitational", "teleport", 9081, &github.ListOptions{
	//reviews, _, err := client.PullRequests.ListReviews(context.Background(), "gravitational", "teleport", 9047, &github.ListOptions{
	//	PerPage: 100,
	//})
	//require.NoError(t, err)
	//fmt.Printf("--> reviews: %v.\n", reviews)
	//for _, review := range reviews {
	//	fmt.Printf("--> %v.\n", review)
	//}

	//pr, _, err := client.PullRequests.Get(context.Background(), "gravitational", "teleport", 9081)
	//require.NoError(t, err)
	//for _, reviewer := range pr.RequestedReviewers {
	//	fmt.Printf("--> reviewer: %v.\n", reviewer)
	//}

	reviewers, _, err := client.PullRequests.ListReviewers(context.Background(), "gravitational", "teleport", 9081, &github.ListOptions{
		PerPage: 100,
	})
	require.NoError(t, err)

	for _, reviewer := range reviewers.Users {
		fmt.Printf("--> reviewer: %v.\n", reviewer)
	}

}
