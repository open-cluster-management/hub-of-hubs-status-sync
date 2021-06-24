module github.com/open-cluster-management/hub-of-hubs-spec-syncer

go 1.16

require (
	github.com/daixiang0/gci v0.2.8 // indirect
	github.com/go-logr/logr v0.1.0
	github.com/jackc/pgx/v4 v4.11.0
	github.com/onsi/gomega v1.10.2 // indirect
	github.com/open-cluster-management/api v0.0.0-20200610161514-939cead3902c
	github.com/open-cluster-management/governance-policy-propagator v0.0.0-20210409191610-0ec1d5a4e19d
	github.com/operator-framework/operator-sdk v0.18.1
	github.com/spf13/pflag v1.0.5
	golang.org/x/tools v0.1.1 // indirect
	k8s.io/apimachinery v0.18.3
	k8s.io/client-go v12.0.0+incompatible
	mvdan.cc/gofumpt v0.1.1 // indirect
	sigs.k8s.io/controller-runtime v0.6.0
)

replace (
	github.com/Azure/go-autorest => github.com/Azure/go-autorest v13.3.2+incompatible // Required by OLM
	golang.org/x/text => golang.org/x/text v0.3.3 // CVE-2020-14040
	howett.net/plist => github.com/DHowett/go-plist v0.0.0-20181124034731-591f970eefbb
	k8s.io/client-go => k8s.io/client-go v0.18.3 // Required by prometheus-operator
)
