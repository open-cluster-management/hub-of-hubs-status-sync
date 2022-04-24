// Copyright (c) 2020 Red Hat, Inc.
// Copyright Contributors to the Open Cluster Management project

package dbsyncers

import (
	"fmt"
	"time"

	"github.com/jackc/pgx/v4/pgxpool"
	policiesv1 "github.com/open-cluster-management/governance-policy-propagator/api/v1"
	"k8s.io/apimachinery/pkg/runtime"
	clustersv1beta1 "open-cluster-management.io/api/cluster/v1beta1"
	placementrulesv1 "open-cluster-management.io/multicloud-operators-subscription/pkg/apis/apps/placementrule/v1"
	appsv1alpha1 "open-cluster-management.io/multicloud-operators-subscription/pkg/apis/apps/v1alpha1"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/scheme"
)

// AddToScheme adds all the resources to be processed to the Scheme.
func AddToScheme(runtimeScheme *runtime.Scheme) error {
	schemeBuilders := []*scheme.Builder{
		policiesv1.SchemeBuilder,
		appsv1alpha1.SchemeBuilder,
		placementrulesv1.SchemeBuilder,
	}

	for _, schemeBuilder := range schemeBuilders {
		if err := schemeBuilder.AddToScheme(runtimeScheme); err != nil {
			return fmt.Errorf("failed to add scheme: %w", err)
		}
	}

	schemeInstallFuncs := []func(s *runtime.Scheme) error{
		clustersv1beta1.Install,
	}

	for _, schemeInstallFunc := range schemeInstallFuncs {
		if err := schemeInstallFunc(runtimeScheme); err != nil {
			return fmt.Errorf("failed to install scheme: %w", err)
		}
	}

	return nil
}

// AddDBSyncers adds all the DBSyncers to the Manager.
func AddDBSyncers(mgr ctrl.Manager, dbConnectionPool *pgxpool.Pool, syncInterval time.Duration) error {
	addDBSyncerFunctions := []func(ctrl.Manager, *pgxpool.Pool, time.Duration) error{
		addPolicyDBSyncer,
		addPlacementRuleStatusDBSyncer,
		addPlacementStatusDBSyncer,
		addPlacementDecisionDBSyncer,
		addSubscriptionStatusStatusDBSyncer,
		addSubscriptionReportDBSyncer,
	}

	for _, addDBSyncerFunction := range addDBSyncerFunctions {
		if err := addDBSyncerFunction(mgr, dbConnectionPool, syncInterval); err != nil {
			return fmt.Errorf("failed to add DB Syncer: %w", err)
		}
	}

	return nil
}
