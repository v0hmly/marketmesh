// Package networkchaos задаёт безопасный lifecycle сетевых fault-сценариев
// MarketMesh.
//
// DockerDriver разрешает заранее заданные immutable ID через минимальные
// docker inspect formats и выполняет только структурированные docker CLI
// arguments без shell. TopologyTargetClient и TopologyDriver потребляют только
// public MM-38 targets/v1, закрепляют локальный Docker endpoint и повторно
// валидируют original binding перед каждой mutation и cleanup. Runner проверяет
// scope непосредственно перед mutation, ограничивает каждую операцию deadline,
// публикует bounded observer lifecycle, собирает diagnostics до cleanup и
// восстанавливает faults в обратном порядке. Resource gate сохраняет любое
// превышение goroutine, heap или bounded queues, а replay manifest фиксирует seed
// и ordered sequence без runtime Docker IDs.
package networkchaos
