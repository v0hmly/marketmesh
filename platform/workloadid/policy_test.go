package workloadid

import (
	"errors"
	"testing"
)

func gatewayInIdentity() Identity {
	return Identity{TrustDomain: "marketmesh.test", Environment: "prod", Role: "gateway-in"}
}

func TestNewPolicyValid(t *testing.T) {
	policy, err := NewPolicy(map[Identity][]string{
		gatewayInIdentity(): {"/marketmesh.user.v1.UserService/GetUser", "/marketmesh.user.v1.UserService/*"},
	})
	if err != nil {
		t.Fatalf("NewPolicy: %v", err)
	}
	if policy == nil {
		t.Fatal("NewPolicy returned nil policy")
	}
}

func TestNewPolicyRejects(t *testing.T) {
	valid := gatewayInIdentity()
	withInstance := valid
	withInstance.Instance = "gateway-in-1"
	badRole := valid
	badRole.Role = "BAD"

	cases := map[string]map[Identity][]string{
		"empty rules":            {},
		"identity with instance": {withInstance: {"/svc/Method"}},
		"invalid identity":       {badRole: {"/svc/Method"}},
		"rule without methods":   {valid: {}},
		"duplicate methods":      {valid: {"/svc/Method", "/svc/Method"}},
		"duplicate wildcard":     {valid: {"/svc/*", "/svc/*"}},
		"method without slash":   {valid: {"svc.Method"}},
		"method without service": {valid: {"/Method"}},
		"method without name":    {valid: {"/svc/"}},
		"method with extra path": {valid: {"/svc/Method/extra"}},
		"empty method":           {valid: {""}},
		"service with spaces":    {valid: {"/my svc/Method"}},
		"wildcard as service":    {valid: {"/*/Method"}},
	}

	for name, rules := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := NewPolicy(rules); !errors.Is(err, ErrInvalidPolicy) {
				t.Fatalf("got %v, want ErrInvalidPolicy", err)
			}
		})
	}
}

func TestPolicyAllow(t *testing.T) {
	policy, err := NewPolicy(map[Identity][]string{
		gatewayInIdentity(): {
			"/marketmesh.user.v1.UserService/GetUser",
			"/marketmesh.auth.v1.AuthService/*",
		},
	})
	if err != nil {
		t.Fatalf("NewPolicy: %v", err)
	}

	t.Run("exact method allowed", func(t *testing.T) {
		if !policy.Allow(gatewayInIdentity(), "/marketmesh.user.v1.UserService/GetUser") {
			t.Fatal("exact rule denied")
		}
	})

	t.Run("wildcard service allowed", func(t *testing.T) {
		if !policy.Allow(gatewayInIdentity(), "/marketmesh.auth.v1.AuthService/RefreshSession") {
			t.Fatal("wildcard rule denied")
		}
	})

	t.Run("wildcard does not cross services", func(t *testing.T) {
		if policy.Allow(gatewayInIdentity(), "/marketmesh.auth.v1.OtherService/RefreshSession") {
			t.Fatal("wildcard leaked into another service")
		}
	})

	t.Run("unknown method denied", func(t *testing.T) {
		if policy.Allow(gatewayInIdentity(), "/marketmesh.user.v1.UserService/DeleteUser") {
			t.Fatal("method without rule allowed")
		}
	})

	t.Run("unknown identity denied", func(t *testing.T) {
		stranger := Identity{TrustDomain: "marketmesh.test", Environment: "prod", Role: "gateway-out"}
		if policy.Allow(stranger, "/marketmesh.user.v1.UserService/GetUser") {
			t.Fatal("identity without rule allowed")
		}
	})

	t.Run("other environment denied", func(t *testing.T) {
		dev := Identity{TrustDomain: "marketmesh.test", Environment: "dev", Role: "gateway-in"}
		if policy.Allow(dev, "/marketmesh.user.v1.UserService/GetUser") {
			t.Fatal("identity from another environment allowed")
		}
	})

	t.Run("instance does not change the match", func(t *testing.T) {
		withInstance := gatewayInIdentity()
		withInstance.Instance = "gateway-in-7f9c"
		if !policy.Allow(withInstance, "/marketmesh.user.v1.UserService/GetUser") {
			t.Fatal("instance-bearing identity did not match the principal rule")
		}
	})

	t.Run("malformed method denied", func(t *testing.T) {
		if policy.Allow(gatewayInIdentity(), "not-a-method") {
			t.Fatal("malformed method allowed")
		}
	})

	t.Run("nil policy denies everything", func(t *testing.T) {
		var nilPolicy *Policy
		if nilPolicy.Allow(gatewayInIdentity(), "/marketmesh.user.v1.UserService/GetUser") {
			t.Fatal("nil policy allowed a call")
		}
	})
}
