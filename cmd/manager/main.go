// Copyright (c) 2020 Red Hat, Inc.
// Copyright Contributors to the Open Cluster Management project

package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"runtime"
	"time"

	"github.com/go-logr/logr"
	"github.com/jackc/pgx/v4/pgxpool"
	"github.com/open-cluster-management/hub-of-hubs-status-sync/pkg/dbsyncers"
	"github.com/operator-framework/operator-sdk/pkg/log/zap"
	sdkVersion "github.com/operator-framework/operator-sdk/version"
	"github.com/spf13/pflag"

	// Import all Kubernetes client auth plugins (e.g. Azure, GCP, OIDC, etc.)
	_ "k8s.io/client-go/plugin/pkg/client/auth"
	ctrl "sigs.k8s.io/controller-runtime"
)

const (
	metricsHost                                  = "0.0.0.0"
	metricsPort                            int32 = 8384
	environmentVariableControllerNamespace       = "POD_NAMESPACE"
	environmentVariableDatabaseURL               = "DATABASE_URL"
	environmentVariableSyncInterval              = "HOH_STATUS_SYNC_INTERVAL"
)

func printVersion(log logr.Logger) {
	log.Info(fmt.Sprintf("Go Version: %s", runtime.Version()))
	log.Info(fmt.Sprintf("Go OS/Arch: %s/%s", runtime.GOOS, runtime.GOARCH))
	log.Info(fmt.Sprintf("Version of operator-sdk: %v", sdkVersion.Version))
}

// function to handle defers with exit, see https://stackoverflow.com/a/27629493/553720.
func doMain() int {
	pflag.CommandLine.AddFlagSet(zap.FlagSet())
	pflag.CommandLine.AddGoFlagSet(flag.CommandLine)
	pflag.Parse()

	ctrl.SetLogger(zap.Logger())
	log := ctrl.Log.WithName("cmd")

	printVersion(log)

	leaderElectionNamespace, found := os.LookupEnv(environmentVariableControllerNamespace)
	if !found {
		log.Error(nil, "Not found:", "environment variable", environmentVariableControllerNamespace)
		return 1
	}

	databaseURL, found := os.LookupEnv(environmentVariableDatabaseURL)
	if !found {
		log.Error(nil, "Not found:", "environment variable", environmentVariableDatabaseURL)
		return 1
	}

	syncIntervalString, found := os.LookupEnv(environmentVariableSyncInterval)
	if !found {
		log.Error(nil, "Not found:", "environment variable", environmentVariableSyncInterval)
		return 1
	}

	syncInterval, err := time.ParseDuration(syncIntervalString)
	if err != nil {
		log.Error(err, "the environment var ", environmentVariableSyncInterval, " is not valid duration")
		return 1
	}

	dbConnectionPool, err := pgxpool.Connect(context.Background(), databaseURL)
	if err != nil {
		log.Error(err, "Failed to connect to the database")
		return 1
	}
	defer dbConnectionPool.Close()

	mgr, err := createManager(leaderElectionNamespace, metricsHost, metricsPort, dbConnectionPool, syncInterval)
	if err != nil {
		log.Error(err, "Failed to create manager")
		return 1
	}

	log.Info("Starting the Cmd.")

	if err := mgr.Start(ctrl.SetupSignalHandler()); err != nil {
		log.Error(err, "Manager exited non-zero")
		return 1
	}

	return 0
}

func createManager(leaderElectionNamespace, metricsHost string, metricsPort int32, dbConnectionPool *pgxpool.Pool,
	syncInterval time.Duration) (ctrl.Manager, error) {
	options := ctrl.Options{
		MetricsBindAddress:      fmt.Sprintf("%s:%d", metricsHost, metricsPort),
		LeaderElection:          true,
		LeaderElectionID:        "hub-of-hubs-status-sync-lock",
		LeaderElectionNamespace: leaderElectionNamespace,
	}

	mgr, err := ctrl.NewManager(ctrl.GetConfigOrDie(), options)
	if err != nil {
		return nil, fmt.Errorf("failed to create a new manager: %w", err)
	}

	if err := dbsyncers.AddToScheme(mgr.GetScheme()); err != nil {
		return nil, fmt.Errorf("failed to add schemes: %w", err)
	}

	if err := dbsyncers.AddDBSyncers(mgr, dbConnectionPool, syncInterval); err != nil {
		return nil, fmt.Errorf("failed to add db syncers: %w", err)
	}

	return mgr, nil
}

func main() {
	os.Exit(doMain())
}
