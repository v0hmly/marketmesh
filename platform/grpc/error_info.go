package grpc

import (
	"errors"
	"regexp"
	"strings"

	"google.golang.org/genproto/googleapis/rpc/errdetails"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// PublicErrorInfo разрешает один фиксированный машинный признак для конкретных
// метода и status code. Значения задаёт composition root, а не входящий запрос.
// Metadata, неизвестные поля и остальные details никогда не переносятся.
type PublicErrorInfo struct {
	Method         string
	Code           codes.Code
	Domain, Reason string
}

var publicDomainPattern = regexp.MustCompile(`^[a-z][a-z0-9_.-]{0,127}$`)
var publicReasonPattern = regexp.MustCompile(`^[A-Z][A-Z0-9_]{0,63}$`)
var publicMethodPattern = regexp.MustCompile(`^/[A-Za-z_][A-Za-z0-9_.]*/[A-Za-z_][A-Za-z0-9_]*$`)

func validatePublicErrorInfo(policies []PublicErrorInfo) error {
	if len(policies) > 32 {
		return errors.New("grpc: too many public error reasons")
	}
	for _, p := range policies {
		if len(p.Method) > 256 || !publicMethodPattern.MatchString(p.Method) || p.Code <= codes.OK || p.Code > codes.Unauthenticated || p.Code == codes.Unknown || !publicDomainPattern.MatchString(p.Domain) || !publicReasonPattern.MatchString(p.Reason) {
			return errors.New("grpc: invalid public error reason policy")
		}
	}
	return nil
}

func sanitizedMethodStatusError(err error, mapper ErrorCodeMapper, method string, policies []PublicErrorInfo) error {
	clean := sanitizedStatusError(err, mapper)
	if clean == nil || len(policies) == 0 {
		return clean
	}
	code := status.Code(clean)
	original, ok := status.FromError(err)
	if !ok || original.Code() != code {
		return clean
	}
	// Bound inspection and never forward the input Any or ErrorInfo itself.
	details := original.Proto().Details
	if len(details) > 16 {
		return clean
	}
	for _, p := range policies {
		if p.Method != method || p.Code != code {
			continue
		}
		for _, raw := range details {
			if raw == nil || len(raw.Value) > 1024 || !strings.HasSuffix(raw.TypeUrl, "/google.rpc.ErrorInfo") {
				continue
			}
			var input errdetails.ErrorInfo
			if raw.UnmarshalTo(&input) != nil || input.Domain != p.Domain || input.Reason != p.Reason {
				continue
			}
			result, e := status.Convert(clean).WithDetails(&errdetails.ErrorInfo{Domain: p.Domain, Reason: p.Reason})
			if e == nil {
				return result.Err()
			}
		}
	}
	return clean
}
