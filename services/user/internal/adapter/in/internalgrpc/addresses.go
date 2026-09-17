package internalgrpc

import (
	"context"
	"errors"
	userv1 "github.com/v0hmly/marketmesh/api/gen/go/user/v1"
	"github.com/v0hmly/marketmesh/services/user/internal/application/addresses"
	"github.com/v0hmly/marketmesh/services/user/internal/application/identity"
	"github.com/v0hmly/marketmesh/services/user/internal/domain/address"
	"google.golang.org/genproto/googleapis/rpc/errdetails"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

type AddressUseCase interface {
	List(context.Context, identity.Principal) (address.Book, error)
	Mutate(context.Context, identity.Principal, addresses.Command) (address.Book, error)
}

// EnableAddresses is called during construction, before the server starts.
func (h *Handler) EnableAddresses(useCase AddressUseCase) error {
	if useCase == nil {
		return errors.New("address grpc: use case required")
	}
	h.addresses = useCase
	return nil
}
func (h *Handler) addressPrincipal(ctx context.Context, method string) (identity.Principal, error) {
	noStore(ctx)
	if h.addresses == nil {
		return identity.Principal{}, status.Error(codes.Unimplemented, "method unavailable")
	}
	return h.authenticate(ctx, method)
}
func addressError(err error) error {
	reason := ""
	code := codes.NotFound
	switch {
	case errors.Is(err, address.ErrInvalid):
		return status.Error(codes.InvalidArgument, "invalid address")
	case errors.Is(err, address.ErrConflict):
		return status.Error(codes.Aborted, "address book version conflict")
	case errors.Is(err, address.ErrNotFound):
		reason = "ADDRESS_NOT_FOUND"
	case errors.Is(err, address.ErrLimit):
		reason = "ADDRESS_LIMIT_REACHED"
		code = codes.ResourceExhausted
	default:
		return mapError(err)
	}
	s, e := status.New(code, "address operation rejected").WithDetails(&errdetails.ErrorInfo{Reason: reason, Domain: "marketmesh.user"})
	if e != nil {
		return status.Error(code, "address operation rejected")
	}
	return s.Err()
}
func fieldsFromWire(f *userv1.AddressFields) (address.Fields, error) {
	if f == nil {
		return address.Fields{}, address.ErrInvalid
	}
	return address.Fields{Recipient: f.GetRecipient(), Phone: f.GetPhone(), Country: f.GetCountry(), PostalCode: f.GetPostalCode(), City: f.GetCity(), StreetHouse: f.GetStreetHouse(), Apartment: f.GetApartment(), Comment: f.GetComment()}, nil
}
func wireBook(b address.Book) *userv1.AddressBook {
	out := &userv1.AddressBook{SubjectId: b.SubjectID.Bytes(), Version: b.Version, Addresses: make([]*userv1.Address, 0, len(b.Addresses))}
	for _, a := range b.Addresses {
		f := a.Fields
		out.Addresses = append(out.Addresses, &userv1.Address{AddressId: a.ID.Bytes(), IsDefault: a.IsDefault, Fields: &userv1.AddressFields{Recipient: f.Recipient, Phone: f.Phone, Country: f.Country, PostalCode: f.PostalCode, City: f.City, StreetHouse: f.StreetHouse, Apartment: f.Apartment, Comment: f.Comment}})
	}
	return out
}
func (h *Handler) ListAddresses(ctx context.Context, r *userv1.ListAddressesRequest) (*userv1.ListAddressesResponse, error) {
	p, e := h.addressPrincipal(ctx, userv1.UserService_ListAddresses_FullMethodName)
	if e != nil {
		return nil, e
	}
	if r == nil {
		return nil, status.Error(codes.InvalidArgument, "invalid request")
	}
	b, e := h.addresses.List(ctx, p)
	if e != nil {
		return nil, addressError(e)
	}
	return &userv1.ListAddressesResponse{Book: wireBook(b)}, nil
}
func (h *Handler) CreateAddress(ctx context.Context, r *userv1.CreateAddressRequest) (*userv1.CreateAddressResponse, error) {
	p, e := h.addressPrincipal(ctx, userv1.UserService_CreateAddress_FullMethodName)
	if e != nil {
		return nil, e
	}
	if r == nil {
		return nil, status.Error(codes.InvalidArgument, "invalid request")
	}
	c := addresses.Command{Operation: addresses.Create, ExpectedVersion: r.GetExpectedBookVersion()}
	c.Fields, e = fieldsFromWire(r.GetFields())
	if e != nil {
		return nil, addressError(e)
	}
	b, e := h.addresses.Mutate(ctx, p, c)
	if e != nil {
		return nil, addressError(e)
	}
	return &userv1.CreateAddressResponse{Book: wireBook(b)}, nil
}
func (h *Handler) UpdateAddress(ctx context.Context, r *userv1.UpdateAddressRequest) (*userv1.UpdateAddressResponse, error) {
	p, e := h.addressPrincipal(ctx, userv1.UserService_UpdateAddress_FullMethodName)
	if e != nil {
		return nil, e
	}
	if r == nil {
		return nil, status.Error(codes.InvalidArgument, "invalid request")
	}
	c := addresses.Command{Operation: addresses.Update, ExpectedVersion: r.GetExpectedBookVersion()}
	c.ID, e = address.NewID(r.GetAddressId())
	if e != nil {
		return nil, addressError(e)
	}
	c.Fields, e = fieldsFromWire(r.GetFields())
	if e != nil {
		return nil, addressError(e)
	}
	b, e := h.addresses.Mutate(ctx, p, c)
	if e != nil {
		return nil, addressError(e)
	}
	return &userv1.UpdateAddressResponse{Book: wireBook(b)}, nil
}
func (h *Handler) DeleteAddress(ctx context.Context, r *userv1.DeleteAddressRequest) (*userv1.DeleteAddressResponse, error) {
	p, e := h.addressPrincipal(ctx, userv1.UserService_DeleteAddress_FullMethodName)
	if e != nil {
		return nil, e
	}
	if r == nil {
		return nil, status.Error(codes.InvalidArgument, "invalid request")
	}
	c := addresses.Command{Operation: addresses.Delete, ExpectedVersion: r.GetExpectedBookVersion()}
	c.ID, e = address.NewID(r.GetAddressId())
	if e != nil {
		return nil, addressError(e)
	}
	b, e := h.addresses.Mutate(ctx, p, c)
	if e != nil {
		return nil, addressError(e)
	}
	return &userv1.DeleteAddressResponse{Book: wireBook(b)}, nil
}
func (h *Handler) SetDefaultAddress(ctx context.Context, r *userv1.SetDefaultAddressRequest) (*userv1.SetDefaultAddressResponse, error) {
	p, e := h.addressPrincipal(ctx, userv1.UserService_SetDefaultAddress_FullMethodName)
	if e != nil {
		return nil, e
	}
	if r == nil {
		return nil, status.Error(codes.InvalidArgument, "invalid request")
	}
	c := addresses.Command{Operation: addresses.SetDefault, ExpectedVersion: r.GetExpectedBookVersion()}
	c.ID, e = address.NewID(r.GetAddressId())
	if e != nil {
		return nil, addressError(e)
	}
	b, e := h.addresses.Mutate(ctx, p, c)
	if e != nil {
		return nil, addressError(e)
	}
	return &userv1.SetDefaultAddressResponse{Book: wireBook(b)}, nil
}
