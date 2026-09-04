package workloadid

import (
	"fmt"
	"strings"
)

// Policy — политика авторизации внутренних RPC: отображение идентичности
// (домен доверия + среда + роль, без экземпляра) на набор разрешённых методов.
// Политика fail-closed: вызов без явного правила отклоняется.
//
// Методы задаются полными gRPC method strings вида "/pkg.Service/Method";
// запись "/pkg.Service/*" разрешает весь сервис. Политика неизменяема после
// конструирования и безопасна для конкурентного использования.
type Policy struct {
	methodsByPrincipal map[principal]map[string]struct{}
}

// principal — ключ политики: идентичность без экземпляра.
type principal struct {
	trustDomain string
	environment string
	role        string
}

// NewPolicy конструирует политику и валидирует её целиком: идентичности
// обязаны быть корректными и без Instance, списки методов непустыми, каждый
// метод — корректным "/pkg.Service/Method" или "/pkg.Service/*", дубликаты
// методов внутри одного правила запрещены. Пустая политика не конструируется.
func NewPolicy(rules map[Identity][]string) (*Policy, error) {
	if len(rules) == 0 {
		return nil, fmt.Errorf("%w: empty rules", ErrInvalidPolicy)
	}

	methodsByPrincipal := make(map[principal]map[string]struct{}, len(rules))
	for identity, methods := range rules {
		if err := identity.Validate(); err != nil {
			return nil, fmt.Errorf("%w: rule identity: %v", ErrInvalidPolicy, err)
		}
		if identity.Instance != "" {
			return nil, fmt.Errorf("%w: rule identity must not pin an instance", ErrInvalidPolicy)
		}
		if len(methods) == 0 {
			return nil, fmt.Errorf("%w: rule has no methods", ErrInvalidPolicy)
		}
		allowed := make(map[string]struct{}, len(methods))
		for _, method := range methods {
			if err := validateMethod(method); err != nil {
				return nil, err
			}
			if _, duplicate := allowed[method]; duplicate {
				return nil, fmt.Errorf("%w: duplicate method in a rule", ErrInvalidPolicy)
			}
			allowed[method] = struct{}{}
		}
		key := principal{
			trustDomain: identity.TrustDomain,
			environment: identity.Environment,
			role:        identity.Role,
		}
		if _, exists := methodsByPrincipal[key]; exists {
			return nil, fmt.Errorf("%w: duplicate rule identity", ErrInvalidPolicy)
		}
		methodsByPrincipal[key] = allowed
	}

	return &Policy{methodsByPrincipal: methodsByPrincipal}, nil
}

// Allow проверяет, разрешён ли идентичности вызов метода. Учитываются точные
// правила и wildcard "/pkg.Service/*". Идентичность сопоставляется без
// Instance. Нулевая политика отклоняет всё.
func (p *Policy) Allow(identity Identity, fullMethod string) bool {
	if p == nil {
		return false
	}
	allowed, known := p.methodsByPrincipal[principal{
		trustDomain: identity.TrustDomain,
		environment: identity.Environment,
		role:        identity.Role,
	}]
	if !known {
		return false
	}
	if _, ok := allowed[fullMethod]; ok {
		return true
	}
	service, _, ok := splitMethod(fullMethod)
	if !ok {
		return false
	}
	_, ok = allowed["/"+service+"/*"]

	return ok
}

// validateMethod проверяет формат правила: "/service/method" либо wildcard
// "/service/*".
func validateMethod(method string) error {
	service, name, ok := splitMethod(method)
	if !ok {
		return fmt.Errorf("%w: method must be /service/method or /service/*", ErrInvalidPolicy)
	}
	if !validMethodChars(service, ".") {
		return fmt.Errorf("%w: service name has forbidden characters", ErrInvalidPolicy)
	}
	if name != "*" && !validMethodChars(name, "") {
		return fmt.Errorf("%w: method name has forbidden characters", ErrInvalidPolicy)
	}

	return nil
}

// splitMethod разбирает полное имя метода на сервис и имя; wildcard "*"
// допустим как имя.
func splitMethod(method string) (service, name string, ok bool) {
	if !strings.HasPrefix(method, "/") {
		return "", "", false
	}
	rest := method[1:]
	slash := strings.IndexByte(rest, '/')
	if slash <= 0 || slash == len(rest)-1 {
		return "", "", false
	}
	service, name = rest[:slash], rest[slash+1:]
	if strings.Contains(name, "/") {
		return "", "", false
	}

	return service, name, true
}

// validMethodChars проверяет, что строка состоит из [a-zA-Z0-9_] плюс
// перечисленных дополнительных символов (например, '.' для пакетов).
func validMethodChars(value string, extra string) bool {
	for _, character := range []byte(value) {
		isLetter := character >= 'a' && character <= 'z' || character >= 'A' && character <= 'Z'
		isDigit := character >= '0' && character <= '9'
		if !isLetter && !isDigit && character != '_' && !strings.ContainsRune(extra, rune(character)) {
			return false
		}
	}

	return true
}
