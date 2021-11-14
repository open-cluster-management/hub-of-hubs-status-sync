[comment]: # ( Copyright Contributors to the Open Cluster Management project )

# Hub-of-Hubs Status Syncer

[![Go Report Card](https://goreportcard.com/badge/github.com/open-cluster-management/hub-of-hubs-status-sync)](https://goreportcard.com/report/github.com/open-cluster-management/hub-of-hubs-status-sync)
[![License](https://img.shields.io/github/license/open-cluster-management/hub-of-hubs-status-sync)](/LICENSE)

The status sync component of [Hub-of-Hubs](https://github.com/open-cluster-management/hub-of-hubs).

:exclamation: Verify that [vadimeisenbergibm/governance-policy-propagator:no_status_update](https://hub.docker.com/layers/156812667/vadimeisenbergibm/governance-policy-propagator/no_status_update/images/sha256-d622f20d24f1e363ffc442f80a24eaafd2a58d953a44d8f8f798e86dbe66ead0) image is used for the governance policy propagator:

```
kubectl get deployment -l component=ocm-policy-propagator -n open-cluster-management --kubeconfig $TOP_HUB_CONFIG  -o jsonpath='{.items[*].spec.template.spec.containers[*].image}'
vadimeisenbergibm/governance-policy-propagator:no_status_update
```
This version of the policy propagator does not update the status of the policy, so it will not interfere with the updates from this controller.

Go to the [Contributing guide](CONTRIBUTING.md) to learn how to get involved.

## Getting Started

The status sync component is implemented as a set of "DBSyncer" components that periodically scan tables in the `status` schema and update required CRs. Note that while these DB syncers are not Kubernetes controllers by definition (they do not reconcile CRs and do not react to changes in the CRs), they can be managed by the controller-runtime `Manager`. They are added to the `Manager` using its `Add(Runnable)` method.

## Environment variables

The following environment variables are required for the most tasks below:

* `REGISTRY`, for example `docker.io/vadimeisenbergibm`.
* `IMAGE_TAG`, for example `v0.1.0`.

## Build to run locally

```
make build
```

## Run Locally

Disable the currently running controller in the cluster (if previously deployed):

```
kubectl scale deployment hub-of-hubs-status-sync -n open-cluster-management --replicas 0
```

Set the following environment variables:

* DATABASE_URL
* WATCH_NAMESPACE
* POD_NAMESPACE

Set the `DATABASE_URL` according to the PostgreSQL URL format: `postgres://YourUserName:YourURLEscapedPassword@YourHostname:5432/YourDatabaseName?sslmode=verify-full&pool_max_conns=50`.

:exclamation: Remember to URL-escape the password, you can do it in bash:

```
python -c "import sys, urllib as ul; print ul.quote_plus(sys.argv[1])" 'YourPassword'
```

`WATCH_NAMESPACE` can be defined empty so the controller will watch all the namespaces.

`POD_NAMESPACE` should usually be `open-cluster-management`

```
./bin/hub-of-hubs-status-sync
```

## Build image

```
make build-images
```

## Deploy to a cluster

1.  Create a secret with your database url:

    ```
    kubectl create secret generic hub-of-hubs-database-secret -n open-cluster-management --from-literal=url=$DATABASE_URL
    ```

1.  Deploy the operator:

    ```
    COMPONENT=$(basename $(pwd)) envsubst < deploy/operator.yaml.template | kubectl apply -n open-cluster-management -f -
    ```
