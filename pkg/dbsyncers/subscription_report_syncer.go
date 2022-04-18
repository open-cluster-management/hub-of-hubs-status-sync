// Copyright (c) 2021 Red Hat, Inc.
// Copyright Contributors to the Open Cluster Management project

package dbsyncers

import (
	"context"
	"fmt"
	"strconv"
	"time"

	"github.com/go-logr/logr"
	"github.com/jackc/pgx/v4/pgxpool"
	"k8s.io/apimachinery/pkg/api/errors"
	appsv1 "open-cluster-management.io/multicloud-operators-subscription/pkg/apis/apps/v1"
	appsv1alpha1 "open-cluster-management.io/multicloud-operators-subscription/pkg/apis/apps/v1alpha1"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

const (
	subscriptionReportsTableName         = "subscription_reports"
	hubOfHubsAggregatedViewAnnotationKey = "hub-of-hubs.open-cluster-management.io/appsView"
	hubOfHubsGlobalView                  = "globalView"
)

func addSubscriptionReportDBSyncer(mgr ctrl.Manager, databaseConnectionPool *pgxpool.Pool,
	syncInterval time.Duration) error {
	err := mgr.Add(&genericDBSyncer{
		syncInterval: syncInterval,
		syncFunc: func(ctx context.Context) {
			syncSubscriptionReports(ctx,
				ctrl.Log.WithName("subscription-reports-db-syncer"),
				databaseConnectionPool,
				mgr.GetClient())
		},
	})
	if err != nil {
		return fmt.Errorf("failed to add subscription reports syncer to the manager: %w", err)
	}

	return nil
}

func syncSubscriptionReports(ctx context.Context, log logr.Logger, databaseConnectionPool *pgxpool.Pool,
	k8sClient client.Client) {
	log.Info("performing sync of subscription-report")

	rows, err := databaseConnectionPool.Query(ctx,
		fmt.Sprintf(`SELECT payload->'metadata'->>'name', payload->'metadata'->>'namespace' 
		FROM spec.%s WHERE deleted = FALSE`, subscriptionsSpecTableName))
	if err != nil {
		log.Error(err, "error in getting subscriptions spec")
		return
	}

	for rows.Next() {
		var name, namespace string

		err := rows.Scan(&name, &namespace)
		if err != nil {
			log.Error(err, "error in select", "table", subscriptionsSpecTableName)
			continue
		}

		go handleSubscriptionReport(ctx, log, databaseConnectionPool, k8sClient, name, namespace)
	}
}

func handleSubscriptionReport(ctx context.Context, log logr.Logger, databaseConnectionPool *pgxpool.Pool,
	k8sClient client.Client, subscriptionName string, subscriptionNamespace string) {
	log.Info("handling a subscription", "name", subscriptionName, "namespace", subscriptionNamespace)

	subscriptionReport, err := getAggregatedSubscriptionReport(ctx, databaseConnectionPool, subscriptionName,
		subscriptionNamespace)
	if err != nil {
		log.Error(err, "failed to get subscription report", "name", subscriptionName,
			"namespace", subscriptionNamespace)

		return
	}

	if subscriptionReport == nil {
		return
	}

	if err := updateSubscriptionReport(ctx, k8sClient, subscriptionReport); err != nil {
		log.Error(err, "failed to update subscription-report status")
	}
}

// returns aggregated SubscriptionReport and error.
func getAggregatedSubscriptionReport(ctx context.Context, databaseConnectionPool *pgxpool.Pool,
	subscriptionName string, subscriptionNamespace string) (*appsv1alpha1.SubscriptionReport, error) {
	rows, err := databaseConnectionPool.Query(ctx,
		fmt.Sprintf(`SELECT payload FROM status.%s
			WHERE payload->'metadata'->>'name'=$1 AND payload->'metadata'->>'namespace'=$2`,
			subscriptionReportsTableName), subscriptionName, subscriptionNamespace)
	if err != nil {
		return nil, fmt.Errorf("error in getting subscription-report statuses from DB - %w", err)
	}

	defer rows.Close()

	var subscriptionReport *appsv1alpha1.SubscriptionReport

	for rows.Next() {
		var leafHubSubscriptionReport appsv1alpha1.SubscriptionReport

		if err := rows.Scan(&leafHubSubscriptionReport); err != nil {
			return nil, fmt.Errorf("error getting subscription reports from DB - %w", err)
		}

		// if not updated yet, clone a report from DB and clean it
		if subscriptionReport == nil {
			subscriptionReport = leafHubSubscriptionReport.DeepCopy()
			updateSubscriptionReportObject(subscriptionReport, appsv1alpha1.SubscriptionReportSummary{},
				[]*appsv1alpha1.SubscriptionReportResult{})
		}

		// update aggregated summary
		updateSubscriptionReportSummary(&subscriptionReport.Summary, &leafHubSubscriptionReport.Summary)
		// update results - assuming that MC names are unique across leaf-hubs, we only need to merge
		subscriptionReport.Results = append(subscriptionReport.Results, leafHubSubscriptionReport.Results...)
	}

	return subscriptionReport, nil
}

func updateSubscriptionReport(ctx context.Context, k8sClient client.Client,
	aggregatedSubscriptionReport *appsv1alpha1.SubscriptionReport) error {
	deployedSubscriptionReport := &appsv1alpha1.SubscriptionReport{}

	err := k8sClient.Get(ctx, client.ObjectKey{
		Name:      aggregatedSubscriptionReport.Name,
		Namespace: aggregatedSubscriptionReport.Namespace,
	},
		deployedSubscriptionReport)
	if err != nil {
		if errors.IsNotFound(err) { // create CR
			if err := k8sClient.Create(ctx, aggregatedSubscriptionReport); err != nil {
				return fmt.Errorf("failed to create subscription-report {name=%s, namespace=%s} - %w",
					aggregatedSubscriptionReport.Name, aggregatedSubscriptionReport.Namespace, err)
			}

			return nil
		}
	}

	// if object exists, clone and update
	originalSubscriptionReport := deployedSubscriptionReport.DeepCopy()

	updateSubscriptionReportObject(deployedSubscriptionReport, aggregatedSubscriptionReport.Summary,
		aggregatedSubscriptionReport.Results)

	err = k8sClient.Patch(ctx, deployedSubscriptionReport, client.MergeFrom(originalSubscriptionReport))
	if err != nil {
		return fmt.Errorf("failed to update subscription-report CR (name=%s, namespace=%s): %w",
			deployedSubscriptionReport.Name, deployedSubscriptionReport.Namespace, err)
	}

	return nil
}

func updateSubscriptionReportSummary(aggregatedSummary *appsv1alpha1.SubscriptionReportSummary,
	reportSummary *appsv1alpha1.SubscriptionReportSummary) {
	aggregatedSummary.Deployed = strconv.Itoa(stringToInt(aggregatedSummary.Deployed) +
		stringToInt(reportSummary.Deployed))

	aggregatedSummary.InProgress = strconv.Itoa(stringToInt(aggregatedSummary.InProgress) +
		stringToInt(reportSummary.InProgress))

	aggregatedSummary.Failed = strconv.Itoa(stringToInt(aggregatedSummary.Failed) +
		stringToInt(reportSummary.Failed))

	aggregatedSummary.PropagationFailed = strconv.Itoa(stringToInt(aggregatedSummary.PropagationFailed) +
		stringToInt(reportSummary.PropagationFailed))

	aggregatedSummary.Clusters = strconv.Itoa(stringToInt(aggregatedSummary.Clusters) +
		stringToInt(reportSummary.Clusters))
}

func updateSubscriptionReportObject(subscriptionReport *appsv1alpha1.SubscriptionReport,
	updatedSummary appsv1alpha1.SubscriptionReportSummary, updatedResults []*appsv1alpha1.SubscriptionReportResult) {
	// assign annotations
	subscriptionReport.Annotations = map[string]string{}
	subscriptionReport.Annotations[hubOfHubsAggregatedViewAnnotationKey] = hubOfHubsGlobalView
	// assign labels
	subscriptionReport.Labels = map[string]string{}
	subscriptionReport.Labels[appsv1.AnnotationHosting] = fmt.Sprintf("%s.%s",
		subscriptionReport.Namespace, subscriptionReport.Name)
	// reset report summary
	subscriptionReport.Summary = updatedSummary
	// reset results
	subscriptionReport.Results = updatedResults
}

func stringToInt(numberString string) int {
	if number, err := strconv.Atoi(numberString); err == nil {
		return number
	}

	return 0
}
