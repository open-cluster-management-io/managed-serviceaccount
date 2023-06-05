# Changelog since v0.2.0
All notable changes to this project will be documented in this file.

## v0.3.0

### New Features
N/A

### Added
* Generate ManagedServiceAccount Client ([#38](https://github.com/open-cluster-management-io/managed-serviceaccount/pull/38) [@Min Kim](https://github.com/yue9944882))
* Add agent-image-pull-secret param to manager ([#40](https://github.com/open-cluster-management-io/managed-serviceaccount/pull/40) [@Hao Liu](https://github.com/TheRealHaoLiu))
* Support addon deployment config ([#54](https://github.com/open-cluster-management-io/managed-serviceaccount/pull/54) [@Wei Liu](https://github.com/skeeey))


### Changes
* Refreh token until remaining lifetime < 20% validity([#49](https://github.com/open-cluster-management-io/managed-serviceaccount/pull/49) [Yang Le](https://github.com/elgnay))
* Use fine grained rbac for addon ([#57](https://github.com/open-cluster-management-io/managed-serviceaccount/pull/57) [@Wei Liu](https://github.com/skeeey))
* Upgrade helm to 3.11.1 ([#60](https://github.com/open-cluster-management-io/managed-serviceaccount/pull/60) [@Jian Zhu](https://github.com/zhujian7))


### Bug Fixes
* Quick restart container when hubconfig changed([#55](https://github.com/open-cluster-management-io/managed-serviceaccount/pull/55) [@xuezhaojun](https://github.com/xuezhaojun))

### Removed & Deprecated
N/A
