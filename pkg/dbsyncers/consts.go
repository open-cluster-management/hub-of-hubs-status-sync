package dbsyncers

// API Info
const (
	appsv1APIGroup          = "apps.open-cluster-management.io/v1"
	clustersv1beta1APIGroup = "cluster.open-cluster-management.io/v1beta1"
	placementKind           = "Placement"
	subscriptionKind        = "Subscription"
)

// DB Tables
const (
	placementsSpecTableName   = "placements"
	placementsStatusTableName = "placements"

	placementDecisionsStatusTableName = "placementdecisions"

	subscriptionsSpecTableName = "subscriptions"

	subscriptionStatusesTableName      = "subscription_statuses"
	subscriptionReportsStatusTableName = "subscription_reports"
)
