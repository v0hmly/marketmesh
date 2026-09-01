// Package networkchaos задаёт безопасный lifecycle сетевых fault-сценариев
// MarketMesh.
//
// DockerDriver разрешает заранее заданные immutable ID через минимальные
// docker inspect formats и выполняет только структурированные docker CLI
// arguments без shell. Runner проверяет scope непосредственно перед mutation,
// ограничивает каждую операцию deadline, собирает diagnostics до cleanup и
// восстанавливает faults в обратном порядке. Resource gate сохраняет любое
// превышение goroutine, heap или bounded queues, а replay manifest фиксирует
// seed и ordered sequence без runtime Docker IDs.
package networkchaos
