// Copyright (c) 2021 Red Hat, Inc.
// Copyright Contributors to the Open Cluster Management project

package dbsyncers

import (
	"context"
	"fmt"
	"time"

	"github.com/go-logr/logr"
	"github.com/jackc/pgx/v4/pgxpool"
	policiesv1 "github.com/open-cluster-management/governance-policy-propagator/pkg/apis/policy/v1"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

type policyDBSyncer struct {
	client                 client.Client
	log                    logr.Logger
	databaseConnectionPool *pgxpool.Pool
	syncInterval           time.Duration
	tableName              string
}

func (syncer *policyDBSyncer) Start(stopChannel <-chan struct{}) error {
	ticker := time.NewTicker(syncer.syncInterval)

	for {
		select {
		case <-stopChannel:
			ticker.Stop()

			syncer.log.Info("stop performing sync for ", syncer.tableName)

			return nil
		case <-ticker.C:
			syncer.sync()
		}
	}
}

func (syncer *policyDBSyncer) sync() {
	syncer.log.Info("performing sync for", syncer.tableName)

	rows, _ := syncer.databaseConnectionPool.Query(context.Background(),
		fmt.Sprintf(`SELECT policy_id, cluster_name, leaf_hub_name, compliance FROM status.%s`, syncer.tableName))

	for rows.Next() {
		var policyID, clusterName, leafHubName string

		err := rows.Scan(&policyID, &clusterName, &leafHubName)
		if err != nil {
			syncer.log.Error(err, "error reading from table %s", syncer.tableName)
			continue
		}

		syncer.log.Info("handling policyID", policyID)
	}

	_ = &policiesv1.Policy{}
}

func addPolicyDBSyncer(mgr ctrl.Manager, databaseConnectionPool *pgxpool.Pool, syncInterval time.Duration) error {
	return mgr.Add(&policyDBSyncer{
		client:                 mgr.GetClient(),
		log:                    ctrl.Log.WithName("policy-db-syncer"),
		databaseConnectionPool: databaseConnectionPool,
		syncInterval:           syncInterval,
		tableName:              "compliance",
	})
}
