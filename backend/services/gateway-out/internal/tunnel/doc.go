// Package tunnel реализует внутреннюю сторону reverse tunnel MarketMesh.
//
// Пакет устанавливает только исходящие mTLS-соединения к gateway-in, строго
// валидирует MM-10 frames и отображает RouteId на локальный неизменяемый
// registry внутренних gRPC-вызовов. Адрес и полное имя метода никогда не
// принимаются из tunnel wire.
package tunnel
