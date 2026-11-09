package main

import (
	"context"
	"fmt"
	"strings"

	"radar/internal/learn"
)

// model retrains (via a rescore) and prints what the model learned.
func (a *app) model(ctx context.Context) error {
	if err := a.rescoreAll(ctx); err != nil {
		return err
	}
	var r learn.Report
	if ok, err := a.store.JSONSetting(ctx, learn.ReportKey, &r); err != nil {
		return err
	} else if !ok {
		fmt.Fprintln(a.out, "No model report yet.")
		return nil
	}
	fmt.Fprintf(a.out, "Learned ranking: %s\n", r.Status)
	fmt.Fprintf(a.out, "Ratings: %d (%d 👍, %d 👎)\n", r.Ratings, r.Liked, r.Disliked)
	if r.ModelAUC == 0 {
		return nil
	}
	fmt.Fprintf(a.out, "Cross-validated ranking quality (AUC, 0.5 = chance): model %.2f, built-in score %.2f\n", r.ModelAUC, r.BaselineAUC)
	fmt.Fprintf(a.out, "How much it trusts the built-in signals: interest match %+.2f, similarity %+.2f\n", r.TrustKeyword, r.TrustSimilar)
	printWeights := func(title string, ws []learn.Weight) {
		if len(ws) == 0 {
			return
		}
		parts := make([]string, len(ws))
		for i, w := range ws {
			parts[i] = fmt.Sprintf("%s (%+.2f)", w.Label, w.Weight)
		}
		fmt.Fprintf(a.out, "%s %s\n", title, strings.Join(parts, ", "))
	}
	printWeights("You tend to like:   ", r.TopLiked)
	printWeights("You tend to dislike:", r.TopDisliked)
	return nil
}
