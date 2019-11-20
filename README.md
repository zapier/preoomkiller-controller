# preoomkiller-controller

`preoomkiller-controller` monitors memory usage metrics for all pods matching
label selector `preoomkiller-enabled=true`, and when memory usage for pods
go above a threshold, specified in pod annotation `preoomkiller.alpha.k8s.zapier.com/memory-threshold`, it attempts to evict the pods. This is a very safe operation as
eviction takes into account **PodDisruptionBudget** and allows for a graceful
termination of the pods.
