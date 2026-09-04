# Kubernetes

Здесь находятся локальная disposable topology и будущие базовые и
окруженческие Kubernetes-манифесты. Сетевые политики должны запрещать
инициирование соединений из DMZ во внутреннюю зону.

## Двух-DC E2E topology

Инструмент MM-44 описан в
[`tools/e2e-topology/README.md`](../../tools/e2e-topology/README.md). Он создаёт
четыре одноразовые OrbStack VM с k3s и не добавляет application workloads или
fault scenarios. Все kube-команды используют только явные `--kubeconfig` и
`--context`; пользовательский current context не читается и не изменяется.

Контракт планового E2E rolling redeploy и rollback tunnel описан в
[`docs/testing/tunnel-rolling-redeploy.md`](../../docs/testing/tunnel-rolling-redeploy.md).

## PostgreSQL client identity

Каждый Go-service передаёт в `platform/postgres.Config.ApplicationName`
стабильную identity формата `<pod>/<namespace>/<cluster>`. Pod name и namespace
берутся через Kubernetes Downward API, а cluster name задаётся явно в
deployment overlay, Helm values или другой конфигурации окружения:

```yaml
env:
  - name: POSTGRES_CLIENT_POD
    valueFrom:
      fieldRef:
        fieldPath: metadata.name
  - name: POSTGRES_CLIENT_NAMESPACE
    valueFrom:
      fieldRef:
        fieldPath: metadata.namespace
  - name: POSTGRES_CLUSTER_NAME
    value: marketmesh-production
```

Composition root обязан прочитать все три значения, проверить их наличие и
собрать строку явно:

```go
applicationName := podName + "/" + namespace + "/" + clusterName
database, err := postgres.New(ctx, postgres.Config{
	ApplicationName: applicationName,
	// RW, RO и Retry опущены.
}, telemetryPipeline)
```

`platform/postgres` не читает environment и Kubernetes API. Компоненты должны
быть печатным ASCII и не содержать `/`. PostgreSQL принимает не более 63 байт,
поэтому deployment обязан соблюдать budget с учётом двух разделителей:

```text
len(pod) + len(namespace) + len(cluster) + 2 <= 63 bytes
```

Превышение budget исправляется сокращением исходных deployment names; молчаливое
усечение identity запрещено. Значение остаётся server-side диагностическим
атрибутом PostgreSQL и не переносится в metric labels или traces.
