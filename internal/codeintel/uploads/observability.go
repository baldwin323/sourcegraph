package uploads

import (
	"fmt"

	"github.com/sourcegraph/sourcegraph/internal/metrics"
	"github.com/sourcegraph/sourcegraph/internal/observation"
)

type operations struct {
	// Not used yet.
	list             *observation.Operation
	get              *observation.Operation
	getBatch         *observation.Operation
	enqueue          *observation.Operation
	delete           *observation.Operation
	uploadsVisibleTo *observation.Operation

	// Commits
	getCommitsVisibleToUpload *observation.Operation
	staleSourcedCommits       *observation.Operation
	updateSourcedCommits      *observation.Operation
	deleteSourcedCommits      *observation.Operation

	// Uploads
	getUploads                     *observation.Operation
	updateUploadRetention          *observation.Operation
	updateUploadsReferenceCounts   *observation.Operation
	softDeleteExpiredUploads       *observation.Operation
	deleteUploadsWithoutRepository *observation.Operation
	deleteUploadsStuckUploading    *observation.Operation
	hardDeleteUploads              *observation.Operation

	// Repositories
	getDirtyRepositories            *observation.Operation
	setRepositoryAsDirty            *observation.Operation
	setRepositoriesForRetentionScan *observation.Operation

	// Packages
	updatePackages *observation.Operation

	// References
	updatePackageReferences *observation.Operation

	// Audit Logs
	deleteOldAuditLogs *observation.Operation
}

func newOperations(observationContext *observation.Context) *operations {
	metrics := metrics.NewREDMetrics(
		observationContext.Registerer,
		"codeintel_uploads",
		metrics.WithLabels("op"),
		metrics.WithCountHelp("Total number of method invocations."),
	)

	op := func(name string) *observation.Operation {
		return observationContext.Operation(observation.Op{
			Name:              fmt.Sprintf("codeintel.uploads.%s", name),
			MetricLabelValues: []string{name},
			Metrics:           metrics,
		})
	}

	return &operations{
		// Not used yet.
		list:             op("List"),
		get:              op("Get"),
		getBatch:         op("GetBatch"),
		enqueue:          op("Enqueue"),
		delete:           op("Delete"),
		uploadsVisibleTo: op("UploadsVisibleTo"),

		// Commits
		getCommitsVisibleToUpload: op("GetCommitsVisibleToUpload"),
		staleSourcedCommits:       op("StaleSourcedCommits"),
		updateSourcedCommits:      op("UpdateSourcedCommits"),
		deleteSourcedCommits:      op("DeleteSourcedCommits"),

		// Uploads
		getUploads:                     op("GetUploads"),
		updateUploadRetention:          op("UpdateUploadRetention"),
		updateUploadsReferenceCounts:   op("UpdateUploadsReferenceCounts"),
		deleteUploadsWithoutRepository: op("DeleteUploadsWithoutRepository"),
		deleteUploadsStuckUploading:    op("DeleteUploadsStuckUploading"),
		softDeleteExpiredUploads:       op("SoftDeleteExpiredUploads"),
		hardDeleteUploads:              op("HardDeleteUploads"),

		// Repositories
		getDirtyRepositories:            op("GetDirtyRepositories"),
		setRepositoryAsDirty:            op("SetRepositoryAsDirty"),
		setRepositoriesForRetentionScan: op("SetRepositoriesForRetentionScan"),

		// Packages
		updatePackages: op("UpdatePackages"),

		// References
		updatePackageReferences: op("UpdatePackageReferences"),

		// Audit Logs
		deleteOldAuditLogs: op("DeleteOldAuditLogs"),
	}
}
