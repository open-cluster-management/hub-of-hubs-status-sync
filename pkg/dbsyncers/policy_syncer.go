// Copyright (c) 2021 Red Hat, Inc.
// Copyright Contributors to the Open Cluster Management project

package dbsyncers

import (
	"context"
	"fmt"
	"time"

	"github.com/go-logr/logr"
	"github.com/jackc/pgx/v4/pgxpool"
	policiesv1 "github.com/open-cluster-management/governance-policy-propagator/api/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

const (
	dbEnumCompliant    = "compliant"
	dbEnumNonCompliant = "non_compliant"

	policiesSpecTableName     = "policies"
	complianceStatusTableName = "compliance"
	placementStatusTableName  = "policies_placement"
)

func addPolicyDBSyncer(mgr ctrl.Manager, databaseConnectionPool *pgxpool.Pool, syncInterval time.Duration) error {
	err := mgr.Add(&genericDBSyncer{
		syncInterval: syncInterval,
		syncFunc: func(ctx context.Context) {
			syncPolicies(ctx,
				ctrl.Log.WithName("policies-db-syncer"),
				databaseConnectionPool, mgr.GetClient(),
				map[string]policiesv1.ComplianceState{
					dbEnumCompliant:    policiesv1.Compliant,
					dbEnumNonCompliant: policiesv1.NonCompliant,
				})
		},
	})
	if err != nil {
		return fmt.Errorf("failed to add policies status syncer to the manager: %w", err)
	}

	return nil
}

func syncPolicies(ctx context.Context, log logr.Logger, databaseConnectionPool *pgxpool.Pool, k8sClient client.Client,
	dbEnumToPolicyComplianceStateMap map[string]policiesv1.ComplianceState) {
	log.Info("performing sync of policies status")

	rows, err := databaseConnectionPool.Query(ctx,
		fmt.Sprintf(`SELECT id, payload->'metadata'->>'name', payload->'metadata'->>'namespace' 
		FROM spec.%s WHERE deleted = FALSE`, policiesSpecTableName))
	if err != nil {
		log.Error(err, "error in getting policies spec")
		return
	}

	defer rows.Close()

	for rows.Next() {
		var id, name, namespace string

		err := rows.Scan(&id, &name, &namespace)
		if err != nil {
			log.Error(err, "error in select", "table", policiesSpecTableName)
			continue
		}

		instance := &policiesv1.Policy{}

		err = k8sClient.Get(ctx, client.ObjectKey{Name: name, Namespace: namespace}, instance)
		if err != nil {
			log.Error(err, "error in getting CR", "name", name, "namespace", namespace)
			continue
		}

		go handlePolicy(ctx, log, databaseConnectionPool, k8sClient, dbEnumToPolicyComplianceStateMap, instance)
	}
}

func handlePolicy(ctx context.Context, log logr.Logger, databaseConnectionPool *pgxpool.Pool,
	k8sClient client.StatusClient, dbEnumToPolicyComplianceStateMap map[string]policiesv1.ComplianceState,
	policy *policiesv1.Policy) {
	compliancePerClusterStatuses, hasNonCompliantClusters, err := getComplianceStatus(ctx, databaseConnectionPool,
		dbEnumToPolicyComplianceStateMap, policy)
	if err != nil {
		log.Error(err, "failed to get compliance status of a policy", "uid", policy.GetUID())
		return
	}

	placementStatus, err := getPlacementStatus(ctx, databaseConnectionPool, policy)
	if err != nil {
		log.Error(err, "failed to get placement status of a policy", "uid", policy.GetUID())
		return
	}

	if err = updateComplianceStatus(ctx, k8sClient, policy, compliancePerClusterStatuses,
		hasNonCompliantClusters, placementStatus); err != nil {
		log.Error(err, "failed to update policy status")
	}
}

// returns array of CompliancePerClusterStatus, whether the policy has any NonCompliant cluster, and error.
func getComplianceStatus(ctx context.Context, databaseConnectionPool *pgxpool.Pool,
	dbEnumToPolicyComplianceStateMap map[string]policiesv1.ComplianceState, policy *policiesv1.Policy,
) ([]*policiesv1.CompliancePerClusterStatus, bool, error) {
	rows, err := databaseConnectionPool.Query(ctx,
		fmt.Sprintf(`SELECT cluster_name,leaf_hub_name,compliance FROM status.%s
			WHERE id=$1 ORDER BY leaf_hub_name, cluster_name`, complianceStatusTableName), string(policy.GetUID()))
	if err != nil {
		return []*policiesv1.CompliancePerClusterStatus{}, false,
			fmt.Errorf("error in getting policy compliance statuses from DB - %w", err)
	}

	var compliancePerClusterStatuses []*policiesv1.CompliancePerClusterStatus

	hasNonCompliantClusters := false

	for rows.Next() {
		var clusterName, leafHubName, complianceInDB string

		if err := rows.Scan(&clusterName, &leafHubName, &complianceInDB); err != nil {
			return []*policiesv1.CompliancePerClusterStatus{}, false,
				fmt.Errorf("error in getting policy compliance statuses from DB - %w", err)
		}

		compliance := dbEnumToPolicyComplianceStateMap[complianceInDB]

		if compliance == policiesv1.NonCompliant {
			hasNonCompliantClusters = true
		}

		compliancePerClusterStatuses = append(compliancePerClusterStatuses, &policiesv1.CompliancePerClusterStatus{
			ComplianceState:  compliance,
			ClusterName:      clusterName,
			ClusterNamespace: clusterName,
		})
	}

	return compliancePerClusterStatuses, hasNonCompliantClusters, nil
}

func getPlacementStatus(ctx context.Context, databaseConnectionPool *pgxpool.Pool,
	policy *policiesv1.Policy) ([]*policiesv1.Placement, error) {
	var placement []*policiesv1.Placement

	if err := databaseConnectionPool.QueryRow(ctx, fmt.Sprintf(`SELECT placement FROM status.%s
			WHERE id=$1`, placementStatusTableName), string(policy.GetUID())).Scan(&placement); err != nil {
		return []*policiesv1.Placement{}, fmt.Errorf("failed to read placement from database: %w", err)
	}

	return placement, nil
}

func updateComplianceStatus(ctx context.Context, k8sClient client.StatusClient, policy *policiesv1.Policy,
	compliancePerClusterStatuses []*policiesv1.CompliancePerClusterStatus, hasNonCompliantClusters bool,
	placementStatus []*policiesv1.Placement) error {
	originalPolicy := policy.DeepCopy()

	policy.Status.Status = compliancePerClusterStatuses
	policy.Status.ComplianceState = ""

	if hasNonCompliantClusters {
		policy.Status.ComplianceState = policiesv1.NonCompliant
	} else if len(compliancePerClusterStatuses) > 0 {
		policy.Status.ComplianceState = policiesv1.Compliant
	}

	policy.Status.Placement = placementStatus

	err := k8sClient.Status().Patch(ctx, policy, client.MergeFrom(originalPolicy))
	if err != nil && !errors.IsNotFound(err) {
		return fmt.Errorf("failed to update policy CR: %w", err)
	}

	return nil
}
