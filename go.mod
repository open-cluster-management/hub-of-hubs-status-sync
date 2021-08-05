module github.com/open-cluster-management/hub-of-hubs-status-sync

go 1.16

require (
	github.com/go-logr/logr v0.2.1
	github.com/go-logr/zapr v0.2.0 // indirect
	github.com/jackc/pgx/v4 v4.11.0
	github.com/onsi/gomega v1.10.2 // indirect
	github.com/open-cluster-management/governance-policy-propagator v0.0.0-20210520203318-a78632de1e26
	github.com/operator-framework/operator-sdk v0.19.4
	github.com/spf13/pflag v1.0.5
	golang.org/x/tools v0.0.0-20201118003311-bd56c0adb394 // indirect
	k8s.io/apimachinery v0.20.5
	k8s.io/client-go v12.0.0+incompatible
	sigs.k8s.io/controller-runtime v0.6.2
)

replace k8s.io/client-go => k8s.io/client-go v0.20.5
