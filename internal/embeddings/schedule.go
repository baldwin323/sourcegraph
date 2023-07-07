package embeddings

import (
	"context"

	"github.com/sourcegraph/sourcegraph/internal/api"
	"github.com/sourcegraph/sourcegraph/internal/database"
	"github.com/sourcegraph/sourcegraph/internal/embeddings/background/repo"
	"github.com/sourcegraph/sourcegraph/internal/gitserver"
)

func ScheduleRepositoriesForEmbedding(
	ctx context.Context,
	repoNames []api.RepoName,
	forceReschedule bool,
	db database.DB,
	repoEmbeddingJobsStore repo.RepoEmbeddingJobsStore,
	gitserverClient gitserver.Client,
) error {
	tx, err := repoEmbeddingJobsStore.Transact(ctx)
	if err != nil {
		return err
	}
	defer func() { err = tx.Done(err) }()

	repoStore := db.Repos()
	for _, repoName := range repoNames {
		// Scope the iteration to an anonymous function, so we can capture all errors and properly rollback tx in defer above.
		err = func() error {
			r, err := repoStore.GetByName(ctx, repoName)
			if err != nil {
				return nil
			}

			refName, latestRevision, err := gitserverClient.GetDefaultBranch(ctx, r.Name, false)
			if err != nil || refName == "" {
				// enqueue a failed job
				_, err = tx.CreateRepoEmbeddingJob(ctx, r.ID, latestRevision, "failed")
				if err != nil {
					return err
				}
				return nil
			}

			// Skip creating a repo embedding job for a repo at revision, if there already exists
			// an identical job that has been completed, or is scheduled to run (processing or queued).
			if !forceReschedule {
				job, _ := tx.GetLastRepoEmbeddingJobForRevision(ctx, r.ID, latestRevision)
				if job.IsRepoEmbeddingJobScheduledOrCompleted() {
					return nil
				}
			}

			_, err = tx.CreateRepoEmbeddingJob(ctx, r.ID, latestRevision, "queued")
			return err
		}()
		if err != nil {
			return err
		}
	}
	return nil
}
