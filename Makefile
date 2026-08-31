SHELL := /bin/bash

.DEFAULT_GOAL := help

WORKSPACE_TOOL := ./tools/go-workspace.sh
PROTOBUF_TOOL := ./tools/protobuf.sh

.PHONY: api-breaking api-bootstrap api-config api-deps api-deps-update api-generate api-generate-check api-lint api-self-test api-verify api-versions arch build fmt fmt-check help test test-race verify vet

## fmt: Отформатировать Go-код во всех модулях
fmt:
	@$(WORKSPACE_TOOL) fmt

## fmt-check: Проверить форматирование Go-кода
fmt-check:
	@$(WORKSPACE_TOOL) fmt-check

## vet: Выполнить go vet во всех модулях
vet:
	@$(WORKSPACE_TOOL) vet

## test: Выполнить тесты во всех модулях
test:
	@$(WORKSPACE_TOOL) test

## test-race: Выполнить тесты с race detector во всех модулях
test-race:
	@$(WORKSPACE_TOOL) test-race

## build: Собрать все Go-пакеты во всех модулях
build:
	@$(WORKSPACE_TOOL) build

## arch: Проверить workspace и архитектурные зависимости
arch:
	@$(WORKSPACE_TOOL) arch

## api-bootstrap: Установить зафиксированные protobuf-инструменты
api-bootstrap:
	@$(PROTOBUF_TOOL) bootstrap

## api-versions: Показать фактические версии protobuf-инструментов
api-versions:
	@$(PROTOBUF_TOOL) versions

## api-config: Проверить easyp.yaml и фиксацию версий
api-config:
	@$(PROTOBUF_TOOL) config

## api-deps: Скачать protobuf-зависимости по easyp.lock
api-deps:
	@$(PROTOBUF_TOOL) deps

## api-deps-update: Обновить protobuf-зависимости и easyp.lock
api-deps-update:
	@$(PROTOBUF_TOOL) deps-update

## api-lint: Проверить protobuf-схемы с помощью EasyP
api-lint:
	@$(PROTOBUF_TOOL) lint

## api-generate: Сгенерировать Go и TypeScript контракты
api-generate:
	@$(PROTOBUF_TOOL) generate

## api-breaking: Проверить protobuf-совместимость относительно dev
api-breaking:
	@$(PROTOBUF_TOOL) breaking

## api-generate-check: Проверить актуальность сгенерированных контрактов
api-generate-check:
	@$(PROTOBUF_TOOL) generate-check

## api-self-test: Проверить генераторы, lint и breaking change
api-self-test:
	@$(PROTOBUF_TOOL) self-test

## api-verify: Выполнить все проверки protobuf toolchain и контрактов
api-verify: api-config api-deps api-lint api-breaking api-generate-check api-self-test

## verify: Выполнить обязательные локальные проверки workspace
verify: api-verify fmt-check vet test-race build arch

## help: Показать доступные команды
help:
	@sed -n 's/^## /  /p' $(MAKEFILE_LIST)
