Test infrastructure:
- 2 k8s clusters with 2 nodes on each
- prometheus in each cluster
- 2 client namespaces on each k8s cluster
- 1 prometheus in docker
- 1 postgresql in docker

Run k3d clusters:
- k3d cluster create --config k3d-cluster-one.yaml --agents-memory 4G
- k3d cluster create --config k3d-cluster-two.yaml --agents-memory 4G

Delete k3d clusters:
- k3d cluster delete cluster-one
- k3d cluster delete cluster-two

Install Argo-Workflows:
- helm install argo-workflows argo/argo-workflows --namespace argo-workflows --create-namespace -f values_cluster_one.yaml
- helm install argo-workflows argo/argo-workflows --namespace argo-workflows --create-namespace -f values_cluster_two.yaml

Add to /etc/hosts:
127.0.0.1 argowf1
127.0.0.1 argowf2

Argo-Workflows RBAC:
- kubectl apply -f ./argowf-rbac.yaml

Install Prometheus with remoteWrite:

Prepare pgsql tables:

Generate code:
