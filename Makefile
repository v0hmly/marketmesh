SHELL := /bin/bash

.DEFAULT_GOAL := help

WORKSPACE_TOOL := ./tools/go-workspace.sh

.PHONY: arch build fmt fmt-check help test test-race verify vet

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

## verify: Выполнить обязательные локальные проверки Go workspace
verify: fmt-check vet test-race build arch

## help: Показать доступные команды
help:
	@sed -n 's/^## /  /p' $(MAKEFILE_LIST)
