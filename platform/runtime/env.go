package runtime

import (
	"errors"
	"fmt"
	"maps"
	"os"
	"strconv"
	"strings"
	"time"
)

// LookupEnv возвращает значение environment variable и признак её наличия.
// Совместима с os.LookupEnv и позволяет тестировать конфигурацию без
// изменения process environment.
type LookupEnv func(name string) (string, bool)

// Env типизированно читает environment через явно переданный LookupEnv.
type Env struct {
	lookup LookupEnv
}

// NewEnv создаёт reader поверх lookup.
func NewEnv(lookup LookupEnv) (Env, error) {
	if lookup == nil {
		return Env{}, errors.New("runtime: environment lookup must not be nil")
	}

	return Env{lookup: lookup}, nil
}

// SystemEnv создаёт reader системного process environment.
func SystemEnv() Env {
	return Env{lookup: os.LookupEnv}
}

// MapEnv создаёт изолированный reader из копии values.
func MapEnv(values map[string]string) Env {
	copied := maps.Clone(values)

	return Env{
		lookup: func(name string) (string, bool) {
			value, found := copied[name]
			return value, found
		},
	}
}

// RequiredString читает обязательную непустую строку и удаляет пробелы
// по краям. Ошибка содержит только имя variable, но не её значение.
func (env Env) RequiredString(name string) (string, error) {
	value, found, err := env.value(name)
	if err != nil {
		return "", err
	}
	value = strings.TrimSpace(value)
	if !found || value == "" {
		return "", fmt.Errorf("runtime: environment variable %s is required", name)
	}

	return value, nil
}

// String читает строку или возвращает fallback, если variable отсутствует
// или пуста. Пробелы по краям удаляются.
func (env Env) String(name string, fallback string) (string, error) {
	value, found, err := env.value(name)
	if err != nil {
		return "", err
	}
	value = strings.TrimSpace(value)
	if !found || value == "" {
		return fallback, nil
	}

	return value, nil
}

// Bool читает bool в формате strconv.ParseBool или возвращает fallback.
func (env Env) Bool(name string, fallback bool) (bool, error) {
	value, found, err := env.value(name)
	if err != nil {
		return false, err
	}
	value = strings.TrimSpace(value)
	if !found || value == "" {
		return fallback, nil
	}

	parsed, err := strconv.ParseBool(value)
	if err != nil {
		return false, fmt.Errorf("runtime: environment variable %s must be a boolean", name)
	}

	return parsed, nil
}

// Duration читает time.Duration или возвращает fallback.
func (env Env) Duration(name string, fallback time.Duration) (time.Duration, error) {
	value, found, err := env.value(name)
	if err != nil {
		return 0, err
	}
	value = strings.TrimSpace(value)
	if !found || value == "" {
		return fallback, nil
	}

	parsed, err := time.ParseDuration(value)
	if err != nil {
		return 0, fmt.Errorf("runtime: environment variable %s must be a duration", name)
	}

	return parsed, nil
}

// PositiveDuration читает положительный time.Duration или возвращает
// положительный fallback.
func (env Env) PositiveDuration(name string, fallback time.Duration) (time.Duration, error) {
	if fallback <= 0 {
		return 0, fmt.Errorf("runtime: fallback for environment variable %s must be positive", name)
	}

	value, err := env.Duration(name, fallback)
	if err != nil {
		return 0, err
	}
	if value <= 0 {
		return 0, fmt.Errorf("runtime: environment variable %s must be positive", name)
	}

	return value, nil
}

// PositiveInt читает положительный int или возвращает положительный
// fallback.
func (env Env) PositiveInt(name string, fallback int) (int, error) {
	if fallback <= 0 {
		return 0, fmt.Errorf("runtime: fallback for environment variable %s must be positive", name)
	}

	value, found, err := env.value(name)
	if err != nil {
		return 0, err
	}
	value = strings.TrimSpace(value)
	if !found || value == "" {
		return fallback, nil
	}

	parsed, err := strconv.Atoi(value)
	if err != nil || parsed <= 0 {
		return 0, fmt.Errorf("runtime: environment variable %s must be a positive integer", name)
	}

	return parsed, nil
}

// PositiveInt64 читает положительный int64 или возвращает положительный
// fallback.
func (env Env) PositiveInt64(name string, fallback int64) (int64, error) {
	if fallback <= 0 {
		return 0, fmt.Errorf("runtime: fallback for environment variable %s must be positive", name)
	}

	value, found, err := env.value(name)
	if err != nil {
		return 0, err
	}
	value = strings.TrimSpace(value)
	if !found || value == "" {
		return fallback, nil
	}

	parsed, err := strconv.ParseInt(value, 10, 64)
	if err != nil || parsed <= 0 {
		return 0, fmt.Errorf("runtime: environment variable %s must be a positive integer", name)
	}

	return parsed, nil
}

// Float64 читает float64 или возвращает fallback.
func (env Env) Float64(name string, fallback float64) (float64, error) {
	value, found, err := env.value(name)
	if err != nil {
		return 0, err
	}
	value = strings.TrimSpace(value)
	if !found || value == "" {
		return fallback, nil
	}

	parsed, err := strconv.ParseFloat(value, 64)
	if err != nil {
		return 0, fmt.Errorf("runtime: environment variable %s must be a number", name)
	}

	return parsed, nil
}

// Secret читает секрет. При required=true пустое или отсутствующее
// значение считается ошибкой. Ошибка никогда не содержит само значение.
func (env Env) Secret(name string, required bool) (Secret, error) {
	value, found, err := env.value(name)
	if err != nil {
		return Secret{}, err
	}
	if required && (!found || value == "") {
		return Secret{}, fmt.Errorf("runtime: secret environment variable %s is required", name)
	}
	if !found {
		return Secret{}, nil
	}

	return newSecret(value), nil
}

func (env Env) value(name string) (string, bool, error) {
	if strings.TrimSpace(name) == "" {
		return "", false, errors.New("runtime: environment variable name must not be empty")
	}
	if env.lookup == nil {
		return "", false, errors.New("runtime: environment is not initialized")
	}

	value, found := env.lookup(name)
	return value, found, nil
}
