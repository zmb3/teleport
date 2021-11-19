package bot

import (
	"fmt"
	"testing"
)

func TestGetReviewers(t *testing.T) {
	reviewers, err := getReviewers("r0mant")
	fmt.Printf("--> reviewers: %v, err: %v.\n", reviewers, err)
}
