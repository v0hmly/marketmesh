// Package probe реализует ограниченное по ресурсам ядро непрерывной E2E-
// проверки tunnel.
//
// Пакет не управляет Kubernetes, fault injection или rollout. Внешний сценарий
// передаёт такие события только как Marker. Транспорт и чтение internal ledger
// подключаются адаптерами после развёртывания disposable E2E workloads.
//
// Записи намеренно не содержат payload, raw error, credentials или PII.
package probe
