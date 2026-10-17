# Kubernetes — примеры

Kubernetes на практике: control plane и планирование, ресурсы и QoS, сеть, хранилище, операторы, GitOps, секреты, hardening.

| Стенд | Описание | Статья |
|---|---|---|
| [`architecture/`](architecture/) | Архитектура Kubernetes на живом кластере: из чего состоит control plane, кто ведущий, что происходит между kubectl apply и запущенным контейнером | [статья](https://khorost.tech/kubernetes/k8s-architecture-control-plane/) |
| [`autoscaling/`](autoscaling/) | Автоскейлинг: HPA, VPA и KEDA на управляемой бизнес-метрике — от источника метрики до решения о масштабировании | [статья](https://khorost.tech/kubernetes/k8s-autoscaling-hpa-vpa-keda/) |
| [`networking/`](networking/) | Сеть в Kubernetes: датаплейн Cilium без kube-proxy, сетевые политики L3/L4 и L7, наблюдаемость через Hubble | 🔜 скоро |
| [`resources/`](resources/) | Ресурсы и QoS: requests/limits, throttling и OOMKill, ограждения namespace (ResourceQuota, LimitRange), рекомендации VPA | [статья](https://khorost.tech/kubernetes/k8s-resources-requests-limits-qos/) |
| [`scheduling/`](scheduling/) | Планирование подов: фильтрация и скоринг узлов, affinity, тейнты и приоритеты — продолжение стенда architecture | [статья](https://khorost.tech/kubernetes/k8s-pod-scheduling-affinity-taints/) |
| [`stateful/`](stateful/) | StatefulSet и хранилище: собственный том на реплику, стабильная идентичность подов, изоляция данных между репликами | [статья](https://khorost.tech/kubernetes/k8s-stateful-storage/) |

---

Навигация: [все категории](../README.md) · [полный список примеров](../INDEX.md)
