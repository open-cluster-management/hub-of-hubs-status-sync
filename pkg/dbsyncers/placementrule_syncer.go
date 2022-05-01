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
	placementrulesv1 "open-cluster-management.io/multicloud-operators-subscription/pkg/apis/apps/placementrule/v1"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

const (
	placementRulesSpecTableName    = "placementrules"
	placementrRulesStatusTableName = "placementrules"
)

func addPlacementRuleStatusDBSyncer(mgr ctrl.Manager, databaseConnectionPool *pgxpool.Pool,
	syncInterval time.Duration) error {
	err := mgr.Add(&genericDBSyncer{
		syncInterval: syncInterval,
		syncFunc: func(ctx context.Context) {
			syncPlacementRules(ctx,
				ctrl.Log.WithName("placementrule-db-syncer"),
				databaseConnectionPool,
				mgr.GetClient())
		},
	})
	if err != nil {
		return fmt.Errorf("failed to add placementrules syncer to the manager: %w", err)
	}

	return nil
}

func syncPlacementRules(ctx context.Context, log logr.Logger, databaseConnectionPool *pgxpool.Pool,
	k8sClient client.Client) {
	log.Info("performing sync of placementrule-status")

	rows, err := databaseConnectionPool.Query(ctx,
		fmt.Sprintf(`SELECT payload->'metadata'->>'name', payload->'metadata'->>'namespace' 
		FROM spec.%s WHERE deleted = FALSE`, placementRulesSpecTableName))
	if err != nil {
		log.Error(err, "error in getting placementrule spec")
		return
	}

	for rows.Next() {
		var name, namespace string

		err := rows.Scan(&name, &namespace)
		if err != nil {
			log.Error(err, "error in select", "table", placementRulesSpecTableName)
			continue
		}

		go handlePlacementRuleStatus(ctx, log, databaseConnectionPool, k8sClient, name, namespace)
	}
}

func handlePlacementRuleStatus(ctx context.Context, log logr.Logger, databaseConnectionPool *pgxpool.Pool,
	k8sClient client.Client, placementRuleName string, placementRuleNamespace string) {
	log.Info("handling a placementrule", "name", placementRuleName, "namespace", placementRuleNamespace)

	placementRule, err := getAggregatedPlacementRules(ctx, databaseConnectionPool, placementRuleName,
		placementRuleNamespace)
	if err != nil {
		log.Error(err, "failed to get aggregated placementrule", "name", placementRuleName,
			"namespace", placementRuleNamespace)

		return
	}

	if placementRule == nil { // no status resources found in DB - placementrule is never created here
		return
	}

	if err := updatePlacementRule(ctx, k8sClient, placementRule); err != nil {
		log.Error(err, "failed to update placementrule status")
	}
}

// returns aggregated PlacementRule (status) and error.
func getAggregatedPlacementRules(ctx context.Context, databaseConnectionPool *pgxpool.Pool,
	placementRuleName string, placementRuleNamespace string) (*placementrulesv1.PlacementRule, error) {
	rows, err := databaseConnectionPool.Query(ctx,
		fmt.Sprintf(`SELECT payload FROM status.%s
			WHERE payload->'metadata'->>'name'=$1 AND payload->'metadata'->>'namespace'=$2`,
			placementrRulesStatusTableName), placementRuleName, placementRuleNamespace)
	if err != nil {
		return nil, fmt.Errorf("error in getting placementrules from DB - %w", err)
	}

	defer rows.Close()

	// build an aggregated placement-rule
	var aggregatedPlacementRule *placementrulesv1.PlacementRule

	for rows.Next() {
		var leafHubPlacementRule placementrulesv1.PlacementRule

		if err := rows.Scan(&leafHubPlacementRule); err != nil {
			return nil, fmt.Errorf("error getting placementrule from DB - %w", err)
		}

		if aggregatedPlacementRule == nil {
			aggregatedPlacementRule = &placementrulesv1.PlacementRule{}
			aggregatedPlacementRule.Name = placementRuleName
			aggregatedPlacementRule.Namespace = placementRuleNamespace
		}

		// assuming that cluster names are unique across the hubs, all we need to do is a complete merge
		aggregatedPlacementRule.Status.Decisions = append(aggregatedPlacementRule.Status.Decisions,
			leafHubPlacementRule.Status.Decisions...)
	}

	return aggregatedPlacementRule, nil
}

func updatePlacementRule(ctx context.Context, k8sClient client.Client,
	aggregatedPlacementRule *placementrulesv1.PlacementRule) error {
	deployedPlacementRule := &placementrulesv1.PlacementRule{}

	err := k8sClient.Get(ctx, client.ObjectKey{
		Name:      aggregatedPlacementRule.Name,
		Namespace: aggregatedPlacementRule.Namespace,
	},
		deployedPlacementRule)
	if err != nil {
		if errors.IsNotFound(err) { // CR getting deleted
			return nil
		}

		return fmt.Errorf("failed to get placementrule {name=%s, namespace=%s} - %w",
			aggregatedPlacementRule.Name, aggregatedPlacementRule.Namespace, err)
	}

	// if object exists, clone and update
	originalPlacementRule := deployedPlacementRule.DeepCopy()

	deployedPlacementRule.Status = aggregatedPlacementRule.Status

	err = k8sClient.Status().Patch(ctx, deployedPlacementRule, client.MergeFrom(originalPlacementRule))
	if err != nil && !errors.IsNotFound(err) {
		return fmt.Errorf("failed to update placementrule CR (name=%s, namespace=%s): %w",
			deployedPlacementRule.Name, deployedPlacementRule.Namespace, err)
	}

	return nil
}
