# VXLAN Fabric Operator
>Manage the point-to-point VXLAN connection between two different Cloud (single node) using kubernetes operator. This project should coordinate with [TN-Manager](https://github.com/ast9501/TN-Manager/tree/v0.2.1).

## Introduction
This project aims to cenrtralize the management the connection between two different cloud.
[TN-Manager](https://github.com/ast9501/TN-Manager/tree/v0.2.1) acts as agent on managed cloud. The operator reconcile logic will activate the vxlan interface on node while new topology object (Custom Resource) be created (also remove the vxlan interface when topology object be deleted).

### Topology object
Define the connection between two cloud with Custom Resource.

## Environment
* For use
  - Go v1.17.13
  - Kubernetes v1.21
* For development
  - Operator-SDK (Go) v1.17

## Deploy
```
make deploy
```
For undeploy the operator:
```
make undeploy
```

## Changelog
### v0.0.x
Initial version for PoC (Proof of Concept):
- Support point-to-point topology