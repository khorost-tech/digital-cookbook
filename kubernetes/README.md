# Kubernetes — примеры

Kubernetes на практике: control plane и планирование, ресурсы и QoS, сеть, хранилище, операторы, GitOps, секреты, hardening.

| Стенд | Описание | Статья |
|---|---|---|
| [`architecture/`](architecture/) | Архитектура Kubernetes на живом кластере: из чего состоит control plane, кто ведущий, что происходит между kubectl apply и запущенным контейнером | [статья](https://khorost.tech/kubernetes/k8s-architecture-control-plane/) |
| [`autoscaling/`](autoscaling/) | Автоскейлинг: HPA, VPA и KEDA на управляемой бизнес-метрике — от источника метрики до решения о масштабировании | [статья](https://khorost.tech/kubernetes/k8s-autoscaling-hpa-vpa-keda/) |
| [`gitops/`](gitops/) | GitOps с ArgoCD: sync, self-heal и progressive delivery — со своим ArgoCD и своим git-сервером как источником правды | [статья](https://khorost.tech/kubernetes/k8s-gitops-argocd-progressive/) |
| [`hardening/`](hardening/) | Hardening подов на контрасте уровней: securityContext, Linux capabilities, Pod Security Standards, seccomp, user namespaces | 🔜 скоро |
| [`networking/`](networking/) | Сеть в Kubernetes: датаплейн Cilium без kube-proxy, сетевые политики L3/L4 и L7, наблюдаемость через Hubble | [статья](https://khorost.tech/kubernetes/k8s-networking-cilium-mesh/) |
| [`operators/`](operators/) | Операторы и CRD на живом production-операторе cert-manager: цепочка Certificate → CertificateRequest → Secret и поддержание желаемого состояния | [статья](https://khorost.tech/kubernetes/k8s-operators-crd/) |
| [`resources/`](resources/) | Ресурсы и QoS: requests/limits, throttling и OOMKill, ограждения namespace (ResourceQuota, LimitRange), рекомендации VPA | [статья](https://khorost.tech/kubernetes/k8s-resources-requests-limits-qos/) |
| [`scheduling/`](scheduling/) | Планирование подов: фильтрация и скоринг узлов, affinity, тейнты и приоритеты — продолжение стенда architecture | [статья](https://khorost.tech/kubernetes/k8s-pod-scheduling-affinity-taints/) |
| [`secrets/`](secrets/) | Секреты в Kubernetes: чем base64 не является, и какие механизмы закрывают разрыв — Sealed Secrets и External Secrets | [статья](https://khorost.tech/kubernetes/k8s-secrets-sealed-external/) |
| [`stateful/`](stateful/) | StatefulSet и хранилище: собственный том на реплику, стабильная идентичность подов, изоляция данных между репликами | [статья](https://khorost.tech/kubernetes/k8s-stateful-storage/) |

---

Навигация: [все категории](../README.md) · [полный список примеров](../INDEX.md)
