// Copyright (c) 2021 Red Hat, Inc.
// Copyright Contributors to the Open Cluster Management project

package dbsyncers

import (
	"context"
	"fmt"
	"time"

	"github.com/go-logr/logr"
	"github.com/jackc/pgx/v4/pgxpool"
	"k8s.io/apimachinery/pkg/api/errors"
	clustersv1beta1 "open-cluster-management.io/api/cluster/v1beta1"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

const (
	placementsSpecTableName   = "placements"
	placementsStatusTableName = "placements"
)

func addPlacementStatusDBSyncer(mgr ctrl.Manager, databaseConnectionPool *pgxpool.Pool,
	syncInterval time.Duration) error {
	err := mgr.Add(&genericDBSyncer{
		syncInterval: syncInterval,
		syncFunc: func(ctx context.Context) {
			syncPlacements(ctx,
				ctrl.Log.WithName("placement-db-syncer"),
				databaseConnectionPool,
				mgr.GetClient())
		},
	})
	if err != nil {
		return fmt.Errorf("failed to add placements syncer to the manager: %w", err)
	}

	return nil
}

func syncPlacements(ctx context.Context, log logr.Logger, databaseConnectionPool *pgxpool.Pool,
	k8sClient client.Client) {
	log.Info("performing sync of placement-status")

	rows, err := databaseConnectionPool.Query(ctx,
		fmt.Sprintf(`SELECT payload->'metadata'->>'name', payload->'metadata'->>'namespace' 
		FROM spec.%s WHERE deleted = FALSE`, placementsSpecTableName))
	if err != nil {
		log.Error(err, "error in getting placement spec")
		return
	}

	for rows.Next() {
		var name, namespace string

		err := rows.Scan(&name, &namespace)
		if err != nil {
			log.Error(err, "error in select", "table", placementsSpecTableName)
			continue
		}

		go handlePlacementStatus(ctx, log, databaseConnectionPool, k8sClient, name, namespace)
	}
}

func handlePlacementStatus(ctx context.Context, log logr.Logger, databaseConnectionPool *pgxpool.Pool,
	k8sClient client.Client, placementName string, placementNamespace string) {
	log.Info("handling a placement", "name", placementName, "namespace", placementNamespace)

	placement, err := getAggregatedPlacements(ctx, databaseConnectionPool, placementName, placementNamespace)
	if err != nil {
		log.Error(err, "failed to get aggregated placement", "name", placementName, "namespace", placementNamespace)
		return
	}

	if placement == nil { // no status resources found in DB - placement is never created here
		return
	}

	if err := updatePlacement(ctx, k8sClient, placement); err != nil {
		log.Error(err, "failed to update placement status")
	}
}

// returns aggregated Placement (statuses) and error.
func getAggregatedPlacements(ctx context.Context, databaseConnectionPool *pgxpool.Pool,
	placementName string, placementNamespace string) (*clustersv1beta1.Placement, error) {
	rows, err := databaseConnectionPool.Query(ctx,
		fmt.Sprintf(`SELECT payload FROM status.%s
			WHERE payload->'metadata'->>'name'=$1 AND payload->'metadata'->>'namespace'=$2`,
			placementsStatusTableName), placementName, placementNamespace)
	if err != nil {
		return nil, fmt.Errorf("error in getting placements from DB - %w", err)
	}

	defer rows.Close()

	// build an aggregated placement
	var aggregatedPlacement *clustersv1beta1.Placement

	for rows.Next() {
		var leafHubPlacement clustersv1beta1.Placement

		if err := rows.Scan(&leafHubPlacement); err != nil {
			return nil, fmt.Errorf("error getting placement from DB - %w", err)
		}

		if aggregatedPlacement == nil {
			aggregatedPlacement = &clustersv1beta1.Placement{}
			aggregatedPlacement.Name = placementName
			aggregatedPlacement.Namespace = placementNamespace
		}

		// assuming that cluster names are unique across the hubs, all we need to do is a complete merge
		aggregatedPlacement.Status.NumberOfSelectedClusters += leafHubPlacement.Status.NumberOfSelectedClusters
	}

	return aggregatedPlacement, nil
}

func updatePlacement(ctx context.Context, k8sClient client.Client,
	aggregatedPlacement *clustersv1beta1.Placement) error {
	deployedPlacement := &clustersv1beta1.Placement{}

	err := k8sClient.Get(ctx, client.ObjectKey{
		Name:      aggregatedPlacement.Name,
		Namespace: aggregatedPlacement.Namespace,
	},
		deployedPlacement)
	if err != nil {
		if errors.IsNotFound(err) { // CR getting deleted
			return nil
		}

		return fmt.Errorf("failed to get placement {name=%s, namespace=%s} - %w",
			aggregatedPlacement.Name, aggregatedPlacement.Namespace, err)
	}

	// if object exists, clone and update
	originalPlacement := deployedPlacement.DeepCopy()

	deployedPlacement.Status.NumberOfSelectedClusters = aggregatedPlacement.Status.NumberOfSelectedClusters

	err = k8sClient.Status().Patch(ctx, deployedPlacement, client.MergeFrom(originalPlacement))
	if err != nil && !errors.IsNotFound(err) {
		return fmt.Errorf("failed to update placement CR (name=%s, namespace=%s): %w",
			deployedPlacement.Name, deployedPlacement.Namespace, err)
	}

	return nil
}
