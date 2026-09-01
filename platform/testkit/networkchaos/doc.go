// Package networkchaos задаёт безопасный lifecycle сетевых fault-сценариев
// MarketMesh.
//
// Пакет не выбирает Docker resources и не выполняет shell-команды. Adapter
// обязан разрешить заранее заданные immutable ID, вернуть проверяемый snapshot
// и применять fault только к указанному interface в disposable test network.
// Runner проверяет scope непосредственно перед mutation, ограничивает каждую
// операцию deadline, собирает diagnostics до cleanup и восстанавливает faults
// в обратном порядке.
package networkchaos
